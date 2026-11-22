package dbbridge

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"academic/backend/internal/model"
	"academic/backend/internal/schedule"
	"github.com/jackc/pgx/v5"
)

type schedules struct{ db *DB }

func (d *DB) Schedules() model.ScheduleRepository { return &schedules{db: d} }

const scheduleColumns = `id,user_id,academic_year,term,teaching_week,start_time,end_time,title,details,source,event_status,mod,series_key,origin_key,active,created_at,updated_at`

func scanSchedule(row pgx.Row) (model.Schedule, error) {
	var s model.Schedule
	var raw []byte
	e := row.Scan(&s.ID, &s.UserID, &s.AcademicYear, &s.Term, &s.TeachingWeek, &s.StartTime, &s.EndTime, &s.Title, &s.Details, &s.Source, &s.EventStatus, &raw, &s.SeriesKey, &s.OriginKey, &s.Active, &s.CreatedAt, &s.UpdatedAt)
	if e != nil {
		return s, storageError(e)
	}
	if json.Unmarshal(raw, &s.Mod) != nil {
		return s, model.ErrInternal
	}
	return schedule.View(s, time.Now()), nil
}
func scheduleUserLock(ctx context.Context, tx pgx.Tx, owner string) error {
	var disabled bool
	e := tx.QueryRow(ctx, `SELECT disabled FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&disabled)
	if e != nil {
		return storageError(e)
	}
	if disabled {
		return model.ErrUnauthenticated
	}
	return nil
}
func insertSchedule(ctx context.Context, tx pgx.Tx, s model.Schedule) (model.Schedule, error) {
	raw, e := json.Marshal(s.Mod)
	if e != nil {
		return s, model.ErrInvalidArgument
	}
	return scanSchedule(tx.QueryRow(ctx, `INSERT INTO schedules(id,user_id,academic_year,term,teaching_week,start_time,end_time,title,details,source,event_status,mod,series_key,origin_key,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING `+scheduleColumns, s.ID, s.UserID, s.AcademicYear, s.Term, s.TeachingWeek, s.StartTime, s.EndTime, s.Title, s.Details, s.Source, s.EventStatus, raw, s.SeriesKey, s.OriginKey, s.Active))
}
func saveSchedule(ctx context.Context, tx pgx.Tx, s model.Schedule) error {
	raw, e := json.Marshal(s.Mod)
	if e != nil {
		return model.ErrInvalidArgument
	}
	_, e = tx.Exec(ctx, `UPDATE schedules SET teaching_week=$3,start_time=$4,end_time=$5,title=$6,details=$7,event_status=$8,mod=$9,active=$10,updated_at=clock_timestamp() WHERE user_id=$1 AND id=$2`, s.UserID, s.ID, s.TeachingWeek, s.StartTime, s.EndTime, s.Title, s.Details, s.EventStatus, raw, s.Active)
	return storageError(e)
}
func (r *schedules) Create(ctx context.Context, owner string, s model.Schedule) (out model.Schedule, err error) {
	if owner == "" || !schedule.ValidSemester(s.Semester) || !schedule.ValidFields(s.ScheduleFields) || (s.Source != "USER" && s.Source != "AGENT") {
		return out, model.ErrInvalidArgument
	}
	s.ID = newID()
	s.UserID = owner
	s.Active = true
	s.SeriesKey = ""
	s.OriginKey = ""
	s.EventStatus = "INCOMPLETE"
	s.Mod = model.ScheduleMod{History: []model.ScheduleChange{}}
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := scheduleUserLock(ctx, tx, owner); e != nil {
			return e
		}
		var e error
		out, e = insertSchedule(ctx, tx, s)
		return e
	})
	return
}
func (r *schedules) Get(ctx context.Context, owner, id string) (model.Schedule, error) {
	args := []any{owner, id}
	where := `user_id=$1 AND id=$2`
	for _, bound := range firstDataBounds(ctx, owner, "schedules", &args) {
		where += " AND " + bound
	}
	return scanSchedule(r.db.pool.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM schedules a WHERE `+where, args...))
}
func (r *schedules) List(ctx context.Context, owner string, q model.ScheduleQuery) ([]model.Schedule, error) {
	if owner == "" || q.Offset < 0 || q.Offset > 100000 || q.Limit < 0 || q.Limit > 1000 {
		return nil, model.ErrInvalidArgument
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	sem := schedule.ValidSemester(q.Semester)
	if q.TeachingWeek != 0 {
		if !sem || q.TeachingWeek < 1 || q.TeachingWeek > 60 {
			return nil, model.ErrInvalidArgument
		}
	} else if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) || q.To.Sub(q.From) > 366*24*time.Hour {
		return nil, model.ErrInvalidArgument
	}
	if (q.AcademicYear != 0 || q.Term != 0) && !sem {
		return nil, model.ErrInvalidArgument
	}
	args := []any{owner, q.AcademicYear, q.Term, q.TeachingWeek, optionalTime(q.From), optionalTime(q.To), q.Limit, q.Offset}
	where := `user_id=$1 AND active AND ($2=0 OR academic_year=$2) AND ($3=0 OR term=$3) AND ($4=0 OR teaching_week=$4) AND ($5::timestamptz IS NULL OR end_time>$5) AND ($6::timestamptz IS NULL OR start_time<$6)`
	for _, bound := range firstDataBounds(ctx, owner, "schedules", &args) {
		where += " AND " + bound
	}
	rows, e := r.db.pool.Query(ctx, `SELECT `+scheduleColumns+` FROM schedules a WHERE `+where+` ORDER BY start_time,id LIMIT $7 OFFSET $8`, args...)
	if e != nil {
		return nil, storageError(e)
	}
	defer rows.Close()
	out := []model.Schedule{}
	for rows.Next() {
		s, e := scanSchedule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, storageError(rows.Err())
}
func optionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func (r *schedules) Update(ctx context.Context, owner, id string, u model.ScheduleUpdate) (out model.Schedule, err error) {
	if owner == "" || id == "" || (u.StartTime == nil && u.EndTime == nil && u.Title == nil && u.Details == nil && u.EventStatus == nil) {
		return out, model.ErrInvalidArgument
	}
	if u.Mode == "" && u.EventStatus != nil && u.StartTime == nil && u.EndTime == nil && u.Title == nil && u.Details == nil {
		u.Mode = "PERMANENT"
	}
	err = r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := scheduleUserLock(ctx, tx, owner); e != nil {
			return e
		}
		selected, e := scanSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE user_id=$1 AND id=$2 AND active`, owner, id))
		if e != nil {
			return e
		}
		targets := []model.Schedule{selected}
		if selected.Source == "TIMETABLE" && u.Mode == "PERMANENT" && (u.StartTime != nil || u.EndTime != nil || u.Title != nil || u.Details != nil) {
			rows, e := tx.Query(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE user_id=$1 AND academic_year=$2 AND term=$3 AND source='TIMETABLE' AND series_key=$4 AND teaching_week>=$5 AND active ORDER BY teaching_week,id`, owner, selected.AcademicYear, selected.Term, selected.SeriesKey, selected.TeachingWeek)
			if e != nil {
				return storageError(e)
			}
			targets = nil
			for rows.Next() {
				s, e := scanSchedule(rows)
				if e != nil {
					rows.Close()
					return e
				}
				if schedule.SameWeekPattern(selected, s) {
					targets = append(targets, s)
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return storageError(e)
			}
		}
		now := time.Now().UTC()
		for _, s := range targets {
			next, e := schedule.Edit(s, selected, u, now)
			if e != nil {
				return e
			}
			if e = saveSchedule(ctx, tx, next); e != nil {
				return e
			}
			if next.ID == selected.ID {
				out = next
			}
		}
		return nil
	})
	return
}
func (r *schedules) Delete(ctx context.Context, owner, id string) error {
	return r.db.transaction(ctx, func(tx pgx.Tx) error {
		if e := scheduleUserLock(ctx, tx, owner); e != nil {
			return e
		}
		s, e := scanSchedule(tx.QueryRow(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE user_id=$1 AND id=$2 AND active`, owner, id))
		if e != nil {
			return e
		}
		if s.Source != "TIMETABLE" {
			_, e = tx.Exec(ctx, `DELETE FROM schedules WHERE user_id=$1 AND id=$2`, owner, id)
			return storageError(e)
		}
		if len(s.Mod.History) >= 256 {
			return model.ErrConflict
		}
		s.Mod.Deleted = true
		s.Active = false
		s.Mod.History = append(s.Mod.History, model.ScheduleChange{Mode: "DELETE", Before: s.ScheduleFields, After: s.ScheduleFields, EffectiveWeek: s.TeachingWeek, OriginalID: s.ID, At: time.Now().UTC()})
		return saveSchedule(ctx, tx, s)
	})
}

func refreshTimetable(ctx context.Context, tx pgx.Tx, owner string, b model.TimetableRefresh) (model.TimetableResult, error) {
	result := model.TimetableResult{Semester: b.Semester, Generated: len(b.Schedules), RetiredModifications: []string{}}
	if !schedule.ValidSemester(b.Semester) || len(b.Schedules) > 20000 {
		return result, model.ErrInvalidArgument
	}
	seen := map[string]bool{}
	for _, s := range b.Schedules {
		if s.Semester != b.Semester || s.Source != "TIMETABLE" || s.OriginKey == "" || s.SeriesKey == "" || seen[s.OriginKey] || !schedule.ValidFields(s.ScheduleFields) || s.Mod.Base == nil {
			return result, model.ErrInvalidArgument
		}
		seen[s.OriginKey] = true
	}
	rows, e := tx.Query(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE user_id=$1 AND academic_year=$2 AND term=$3 AND source='TIMETABLE' ORDER BY id`, owner, b.AcademicYear, b.Term)
	if e != nil {
		return result, storageError(e)
	}
	old := map[string]model.Schedule{}
	for rows.Next() {
		s, e := scanSchedule(rows)
		if e != nil {
			rows.Close()
			return result, e
		}
		old[s.OriginKey] = s
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, storageError(e)
	}
	now := time.Now().UTC()
	policies := map[string][]model.Schedule{}
	for _, candidate := range old {
		if candidate.Mod.Permanent != nil {
			policies[candidate.SeriesKey] = append(policies[candidate.SeriesKey], candidate)
		}
	}
	for _, s := range b.Schedules {
		s.UserID = owner
		previous, existed := old[s.OriginKey]
		if !existed || previous.Mod.Permanent == nil {
			var template model.Schedule
			for _, candidate := range policies[s.SeriesKey] {
				if candidate.TeachingWeek <= s.TeachingWeek && candidate.Mod.Permanent != nil && candidate.TeachingWeek > template.TeachingWeek && schedule.SameWeekPattern(candidate, s) {
					template = candidate
				}
			}
			if template.Mod.Permanent != nil {
				inherited, e := schedule.InheritPermanent(s, template, now)
				if e != nil {
					return result, e
				}
				if existed {
					previous.Mod.Permanent = inherited.Mod.Permanent
					if len(previous.Mod.History)+len(inherited.Mod.History) > 256 {
						return result, model.ErrConflict
					}
					previous.Mod.History = append(previous.Mod.History, inherited.Mod.History...)
					old[s.OriginKey] = previous
				} else {
					s = inherited
				}
			}
		}
		if previous, ok := old[s.OriginKey]; ok {
			next, e := schedule.Rebase(previous, s, now)
			if e != nil {
				return result, e
			}
			if e = saveSchedule(ctx, tx, next); e != nil {
				return result, e
			}
		} else {
			s.ID = newID()
			if _, e = insertSchedule(ctx, tx, s); e != nil {
				return result, e
			}
		}
	}
	for key, s := range old {
		if seen[key] {
			continue
		}
		if !s.Active {
			continue
		}
		if s.Mod.Permanent != nil || s.Mod.Temporary != nil || s.Mod.Deleted {
			result.RetiredModifications = append(result.RetiredModifications, s.ID)
		}
		if len(s.Mod.History) >= 256 {
			return result, model.ErrConflict
		}
		s.Active = false
		s.Mod.History = append(s.Mod.History, model.ScheduleChange{Mode: "SYNC_REMOVED", Before: s.ScheduleFields, After: s.ScheduleFields, EffectiveWeek: s.TeachingWeek, OriginalID: s.ID, At: now})
		if e = saveSchedule(ctx, tx, s); e != nil {
			return result, e
		}
	}
	sort.Strings(result.RetiredModifications)
	return result, nil
}

var _ model.ScheduleRepository = (*schedules)(nil)
