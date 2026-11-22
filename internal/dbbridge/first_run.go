package dbbridge

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"
	"strings"
	"time"
)

const schemaV17 = `CREATE TABLE first_runs(owner_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, batch_id text NOT NULL REFERENCES control_batches(id), packet jsonb NOT NULL);
ALTER TABLE control_events ADD COLUMN after_first_click boolean NOT NULL DEFAULT false;
ALTER TABLE control_batches ADD COLUMN after_first_click boolean NOT NULL DEFAULT false;
CREATE INDEX control_auto_pending ON control_batches(owner_id) WHERE status='pending' AND after_first_click;
ALTER TABLE dws_messages ADD COLUMN sent_at timestamptz;
CREATE INDEX dws_messages_sent ON dws_messages(user_id,sent_at DESC,id) WHERE sent_at IS NOT NULL;`

func sourceSentTime(content []byte) *time.Time {
	var v struct {
		Create json.RawMessage `json:"createTime"`
	}
	if json.Unmarshal(content, &v) != nil {
		return nil
	}
	var s string
	if json.Unmarshal(v.Create, &s) != nil {
		s = string(v.Create)
	}
	return parseAcademicTime(s, "createTime").Value
}

// Serialize the short capture and baseline transactions, not the model run. Events
// committed after the snapshot are marked even if their refresh round began earlier.
func firstLock(ctx context.Context, tx pgx.Tx, owner string) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,730022417))`, owner)
	return e
}
func (r *controls) FirstStatus(ctx context.Context, owner string) (s model.FirstStatus, e error) {
	e = r.db.pool.QueryRow(ctx, `SELECT b.status,b.id,b.error_code FROM first_runs f JOIN control_batches b ON b.id=f.batch_id WHERE f.owner_id=$1`, owner).Scan(&s.State, &s.Batch, &s.Error)
	if errors.Is(e, pgx.ErrNoRows) {
		return model.FirstStatus{State: "not_started"}, nil
	}
	return s, storageError(e)
}
func (r *controls) StartFirst(ctx context.Context, owner string) (s model.FirstStatus, err error) {
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := firstLock(ctx, tx, owner); e != nil {
			return e
		}
		var b string
		var st string
		e := tx.QueryRow(ctx, `SELECT b.id,b.status FROM first_runs f JOIN control_batches b ON b.id=f.batch_id WHERE f.owner_id=$1 FOR UPDATE OF b`, owner).Scan(&b, &st)
		exists := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if exists && (st == "succeeded" || st == "running" || st == "queued") {
			s = model.FirstStatus{Batch: b, State: st}
			return nil
		}
		// Do not activate automatic processing without an enabled Learner.
		var ready bool
		if e = tx.QueryRow(ctx, `SELECT count(*)=1 FROM agent_instances WHERE owner_user_id=$1 AND status='enabled' AND config->>'agent_type'='learner'`, owner).Scan(&ready); e != nil {
			return e
		}
		if !ready {
			return model.ErrConflict
		}
		if !exists {
			p, e := firstPacket(ctx, tx, owner)
			if e != nil {
				return e
			}
			if len(p.Targets) == 0 {
				return model.ErrNotFound
			}
			raw, e := json.Marshal(p)
			if e != nil {
				return e
			}
			round := newID()
			b = newID()
			if _, e = tx.Exec(ctx, `INSERT INTO control_rounds(id,status,finished_at) VALUES($1,'completed',now())`, round); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO control_batches(id,round_id,owner_id,source,stage,status,changes,change_count) VALUES($1,$2,$3,'first_run','first_run','queued','[]',0)`, b, round, owner); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO first_runs(owner_id,batch_id,packet) VALUES($1,$2,$3)`, owner, b, raw); e != nil {
				return e
			}
		} else {
			var prior model.FirstPacket
			var raw []byte
			if e = tx.QueryRow(ctx, `SELECT packet FROM first_runs WHERE owner_id=$1`, owner).Scan(&raw); e != nil {
				return e
			}
			if e = json.Unmarshal(raw, &prior); e != nil {
				return e
			}
			if prior.Scope != model.FirstEvidenceScope {
				if len(prior.Targets) == 0 {
					prior, e = firstPacket(ctx, tx, owner)
					if e != nil {
						return e
					}
					if len(prior.Targets) == 0 {
						return model.ErrNotFound
					}
				} else {
					prior.Scope = model.FirstEvidenceScope
				}
				if _, e = tx.Exec(ctx, `UPDATE first_runs SET packet=$2 WHERE owner_id=$1`, owner, prior); e != nil {
					return e
				}
				// Revisit courses ignored by the old name prefilter. Keep the old
				// runs and preserve completed course work.
				if _, e = tx.Exec(ctx, `UPDATE control_items SET status='PENDING',record=jsonb_set(record,'{status}', '"PENDING"') WHERE batch_id=$1 AND owner_id=$2 AND status='IGNORED'`, b, owner); e != nil {
					return e
				}
			}
			if _, e = tx.Exec(ctx, `UPDATE control_batches SET status='queued',error_code='',updated_at=now() WHERE id=$1`, b); e != nil {
				return e
			}
		}
		s = model.FirstStatus{Batch: b, State: "queued"}
		return nil
	})
	return
}

const usablePublication = `(a.published_time->>'source_field' IN ('sendTime','publishTime','publishedTime','firstPublishTime','published_at','publish_time','postTime','replyTime','create_time','createTime','createtime','assignment_list:发布时间','assignment_list:发布于') OR (a.source='mooc' AND a.published_time->>'source_field'='releaseTime'))`

func firstPacket(ctx context.Context, tx pgx.Tx, owner string) (p model.FirstPacket, err error) {
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&p.AsOf); err != nil {
		return
	}
	p.Since = p.AsOf.Add(-7 * 24 * time.Hour)
	p.Missing = map[string]int{}
	p.Courses = []model.FirstEvidence{}
	p.Scope = model.FirstEvidenceScope
	year, term := firstSemester(p.AsOf)
	// Snapshot only whitelisted catalog fields (never entire raw payloads or messages).
	const src = ` FROM platform_records r JOIN platform_connections c ON c.user_id=r.user_id AND c.id=r.connection_id WHERE r.user_id=$1 AND c.enabled AND c.state='active' `
	rows, e := tx.Query(ctx, `SELECT r.id::text,r.source,r.connection_id,r.external_id,left(r.content->>'title',140), left(concat_ws('；',r.content#>>'{fields,teacher_raw}',r.content#>>'{fields,schedule_raw}',r.content#>>'{fields,location_raw}'),320)`+src+` AND r.kind='course' AND r.source='jwxt' AND r.content#>>'{fields,academic_year}'=$2 AND r.content#>>'{fields,term}'=$3 ORDER BY r.id`, owner, fmt.Sprint(year), fmt.Sprint(term))
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var v model.FirstEvidence
		var conn, id string
		if e = rows.Scan(&v.Ref, &v.Source, &conn, &id, &v.Title, &v.Text); e != nil {
			rows.Close()
			return p, e
		}
		v.Ref = "platform_records:" + v.Ref
		v.Kind = "selected_course"
		v.Course = v.Title
		v.Entity = "source=jwxt|connection=" + conn + "|course_ref=" + id
		p.Courses = append(p.Courses, v)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return p, e
	}

	rows, e = tx.Query(ctx, `SELECT a.source,a.connection_id,a.course_ref,a.id,left(a.name,140),left(a.teacher,120) FROM academic_offerings a JOIN platform_connections c ON c.user_id=a.user_id AND c.id=a.connection_id WHERE a.user_id=$1 AND c.enabled AND c.state='active' ORDER BY a.source,a.id`, owner)
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var v model.FirstEvidence
		var conn, id string
		if e = rows.Scan(&v.Source, &conn, &id, &v.Ref, &v.Title, &v.Text); e != nil {
			rows.Close()
			return p, e
		}
		v.Kind = "platform_course"
		v.Course = v.Title
		v.Entity = "source=" + v.Source + "|connection=" + conn + "|course_ref=" + id
		v.Ref = "academic_offerings:" + v.Ref
		p.Courses = append(p.Courses, v)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return p, e
	}
	rows, e = tx.Query(ctx, `SELECT DISTINCT ON (series_key) id,left(title,140),left(details,180),weeks.mod->>'weeks_raw',to_char(start_time AT TIME ZONE 'Asia/Shanghai','ID HH24:MI'),to_char(end_time AT TIME ZONE 'Asia/Shanghai','HH24:MI') FROM schedules weeks WHERE user_id=$1 AND active AND source='TIMETABLE' AND academic_year=$2 AND term=$3 ORDER BY series_key,start_time`, owner, year, term)
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var v model.FirstEvidence
		var details, start, end string
		var weeks *string
		if e = rows.Scan(&v.Ref, &v.Title, &details, &weeks, &start, &end); e != nil {
			rows.Close()
			return p, e
		}
		v.Ref = "schedules:" + v.Ref
		v.Kind = "timetable"
		v.Source = "jwxt"
		v.Text = "周" + start + "–" + end + "；" + details
		if weeks != nil {
			v.Text += "；" + *weeks
		}
		p.Courses = append(p.Courses, v)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return p, e
	}
	selected := map[string]bool{}
	p.Targets = []model.FirstCourse{}
	for _, course := range p.Courses {
		if course.Kind != "selected_course" || selected[course.Entity] || strings.TrimSpace(course.Title) == "" {
			continue
		}
		selected[course.Entity] = true
		target := model.FirstCourse{Course: course, Context: []model.FirstEvidence{}}
		for _, candidate := range p.Courses {
			if candidate.Kind != "selected_course" && firstCourseName(candidate.Title) == firstCourseName(course.Title) {
				target.Context = append(target.Context, candidate)
			}
		}
		p.Targets = append(p.Targets, target)
	}
	p.Courses = nil
	err = firstPublicationGaps(ctx, tx, owner, &p)
	return p, err
}

func (r *controls) FirstInput(ctx context.Context, owner, batch string) (p model.FirstPacket, err error) {
	var raw []byte
	err = r.db.pool.QueryRow(ctx, `SELECT packet FROM first_runs WHERE owner_id=$1 AND batch_id=$2`, owner, batch).Scan(&raw)
	if err != nil {
		return p, storageError(err)
	}
	err = json.Unmarshal(raw, &p)
	return
}

// Treat a presentation suffix as a discovery hint, not as confirmed enrollment.
func firstCourseName(title string) string {
	title = strings.Join(strings.Fields(norm.NFKC.String(title)), "")
	if i := strings.LastIndex(title, "(第"); i > 0 && strings.HasSuffix(title, "期)") {
		n := strings.TrimSuffix(title[i+len("(第"):], "期)")
		if n != "" && strings.Trim(n, "0123456789") == "" {
			title = title[:i]
		}
	}
	return title
}
