package dbbridge

import (
	"bytes"
	"context"
	"encoding/json"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

// ControlCourseBatchBytes is the raw event budget for one complete course unit.
// Oversized units retain every event in a blocked batch and require manual review.
const ControlCourseBatchBytes = 700 * 1024

type controlSealEvent struct {
	id, round, owner, source string
	raw                      []byte
	change                   model.ControlChange
}
type controlSealGroup struct {
	first  controlSealEvent
	scope  *model.ControlBatchScope
	events []controlSealEvent
}

func sealControlBatches(ctx context.Context, tx pgx.Tx) error {
	// Do not limit events: a global row limit could split a course between Seal calls.
	rows, err := tx.Query(ctx, `SELECT e.id,e.round_id,e.owner_id,e.source,e.payload FROM control_events e JOIN control_rounds r ON r.id=e.round_id WHERE e.batch_id IS NULL AND r.status<>'refreshing' ORDER BY e.round_id,e.owner_id,e.source,e.created_at,e.id`)
	if err != nil {
		return err
	}
	all := []controlSealEvent{}
	for rows.Next() {
		var v controlSealEvent
		if err = rows.Scan(&v.id, &v.round, &v.owner, &v.source, &v.raw); err != nil {
			rows.Close()
			return err
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for len(all) > 0 {
		n := 1
		for n < len(all) && all[n].round == all[0].round && all[n].owner == all[0].owner && all[n].source == all[0].source {
			n++
		}
		events := all[:n]
		groups := []controlSealGroup{}
		if events[0].source == "chaoxing" || events[0].source == "mooc" {
			resolver, err := loadControlCourseResolver(ctx, tx, events)
			if err != nil {
				return err
			}
			indexes := map[[2]string]int{}
			for _, v := range events {
				scope := resolver.resolve(v.change)
				key := [2]string{scope.ConnectionID, scope.CourseRef}
				idx, ok := indexes[key]
				if !ok {
					idx = len(groups)
					indexes[key] = idx
					groups = append(groups, controlSealGroup{first: v, scope: scope})
				}
				groups[idx].events = append(groups[idx].events, v)
			}
		} else {
			groups = append(groups, controlSealGroup{first: events[0], events: events})
		}
		for _, g := range groups {
			for len(g.events) > 0 {
				count, size := 0, 0
				course := g.scope != nil && g.scope.Kind == "course"
				for count < len(g.events) {
					if !course && (count == 100 || (count > 0 && size+len(g.events[count].raw) > 44000)) {
						break
					}
					size += len(g.events[count].raw)
					count++
				}
				status, code := "pending", ""
				if course && size > ControlCourseBatchBytes {
					status, code = "blocked", "COURSE_INPUT_TOO_LARGE"
				} else if !course && size > 44000 {
					status, code = "blocked", "EVENT_TOO_LARGE"
				}
				chunk, ids := make([]json.RawMessage, count), make([]string, count)
				for i, v := range g.events[:count] {
					chunk[i] = v.raw
					ids[i] = v.id
				}
				raw, err := json.Marshal(chunk)
				if err != nil {
					return err
				}
				var scope []byte
				if g.scope != nil {
					scope, err = json.Marshal(g.scope)
					if err != nil {
						return err
					}
				}
				id := newID()
				if _, err = tx.Exec(ctx, `INSERT INTO control_batches(id,round_id,owner_id,source,status,changes,change_count,error_code,batch_scope)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, g.first.round, g.first.owner, g.first.source, status, raw, count, code, scope); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `UPDATE control_events SET batch_id=$1 WHERE id=ANY($2::text[]) AND batch_id IS NULL`, id, ids); err != nil {
					return err
				}
				g.events = g.events[count:]
			}
		}
		all = all[n:]
	}
	return nil
}

type controlCourseRecord struct{ connection, parent, external, kind string }
type controlCourseResolver struct {
	source   string
	records  map[string]controlCourseRecord
	courses  map[[2]string]*model.ControlBatchScope
	evidence map[[2]string]*model.ControlBatchScope
}

func (r *controlCourseResolver) addCourse(connection, alias string, scope *model.ControlBatchScope) {
	if alias == "" {
		return
	}
	key := [2]string{connection, alias}
	if prior, ok := r.courses[key]; ok && (prior == nil || prior.CourseRef != scope.CourseRef) {
		r.courses[key] = nil
		return
	}
	r.courses[key] = scope
}
func loadControlCourseResolver(ctx context.Context, tx pgx.Tx, events []controlSealEvent) (*controlCourseResolver, error) {
	r := &controlCourseResolver{source: events[0].source, records: map[string]controlCourseRecord{}, courses: map[[2]string]*model.ControlBatchScope{}, evidence: map[[2]string]*model.ControlBatchScope{}}
	ids, connections := []string{}, []string{}
	seen := map[string]bool{}
	for i := range events {
		if err := json.Unmarshal(events[i].raw, &events[i].change); err != nil {
			return nil, err
		}
		c := events[i].change
		ids = append(ids, c.RecordID)
		if c.ConnectionID != "" && !seen[c.ConnectionID] {
			seen[c.ConnectionID] = true
			connections = append(connections, c.ConnectionID)
		}
	}
	rows, err := tx.Query(ctx, `SELECT id::text,connection_id,parent_id,external_id,kind FROM platform_records WHERE user_id=$1 AND source=$2 AND id::text=ANY($3::text[])`, events[0].owner, r.source, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var record controlCourseRecord
		if err = rows.Scan(&id, &record.connection, &record.parent, &record.external, &record.kind); err != nil {
			rows.Close()
			return nil, err
		}
		r.records[id] = record
		if !seen[record.connection] {
			seen[record.connection] = true
			connections = append(connections, record.connection)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT connection_id,id,course_ref,name,native_course_id,native_class_id,native_term_id FROM academic_offerings WHERE user_id=$1 AND source=$2 AND connection_id=ANY($3::text[])`, events[0].owner, r.source, connections)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		scope := &model.ControlBatchScope{Kind: "course"}
		var course, class, term string
		if err = rows.Scan(&scope.ConnectionID, &scope.OfferingID, &scope.CourseRef, &scope.CourseName, &course, &class, &term); err != nil {
			rows.Close()
			return nil, err
		}
		r.addCourse(scope.ConnectionID, scope.CourseRef, scope)
		r.addCourse(scope.ConnectionID, academicRef(r.source, course, class, term), scope)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Archived course records are also confirmation when their rebuildable offering
	// is absent. Native owner course/class/term fields must establish the identity.
	rows, err = tx.Query(ctx, `SELECT connection_id,external_id,content FROM platform_records WHERE user_id=$1 AND source=$2 AND kind='course' AND connection_id=ANY($3::text[]) ORDER BY id`, events[0].owner, r.source, connections)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var connection, external string
		var raw []byte
		if err = rows.Scan(&connection, &external, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		v, decodeErr := decodeAcademicRaw(model.PlatformRecord{Source: r.source, Content: raw})
		if decodeErr != nil {
			continue
		}
		c, cl, t := controlCourseNative(v.fields, r.source)
		canonical := academicRef(r.source, c, cl, t)
		if canonical == "" || external == "" {
			continue
		}
		scope := r.courses[[2]string{connection, external}]
		if scope == nil {
			scope = &model.ControlBatchScope{Kind: "course", ConnectionID: connection, CourseRef: external, CourseName: v.title}
		}
		r.addCourse(connection, external, scope)
		r.addCourse(connection, canonical, scope)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `SELECT e.connection_id,e.record_id::text,o.id,o.course_ref,o.name FROM academic_item_evidence e JOIN academic_items i ON i.user_id=e.user_id AND i.connection_id=e.connection_id AND i.id=e.item_id JOIN academic_offerings o ON o.user_id=i.user_id AND o.connection_id=i.connection_id AND o.id=i.offering_id WHERE e.user_id=$1 AND i.source=$2 AND o.source=$2 AND e.record_id::text=ANY($3::text[]) AND e.connection_id=ANY($4::text[])`, events[0].owner, r.source, ids, connections)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		scope := &model.ControlBatchScope{Kind: "course"}
		var id string
		if err = rows.Scan(&scope.ConnectionID, &id, &scope.OfferingID, &scope.CourseRef, &scope.CourseName); err != nil {
			rows.Close()
			return nil, err
		}
		key := [2]string{scope.ConnectionID, id}
		if prior, ok := r.evidence[key]; ok && (prior == nil || prior.CourseRef != scope.CourseRef) {
			r.evidence[key] = nil
		} else {
			r.evidence[key] = scope
		}
	}
	err = rows.Err()
	rows.Close()
	return r, err
}

// Owner route fields are authoritative. Active/source terms identify fetched
// content and must never stand in for an enrolled MOOC term.
func controlCourseNative(f map[string]any, source string) (course, class, term string) {
	if source == "chaoxing" {
		return academicField(f, "course_id", "courseId"), academicField(f, "class_id", "classId"), ""
	}
	return academicField(f, "routeCourseId", "route_course_id", "courseId", "course_id"), "", academicField(f, "routeTermId", "route_term_id", "termId", "term_id")
}
func (r *controlCourseResolver) resolve(c model.ControlChange) *model.ControlBatchScope {
	connection := c.ConnectionID
	record, exists := r.records[c.RecordID]
	if connection == "" && exists {
		connection = record.connection
	}
	unknown := &model.ControlBatchScope{Kind: "unassociated", ConnectionID: connection}
	if connection == "" {
		return unknown
	}
	evidence, hasEvidence := r.evidence[[2]string{connection, c.RecordID}]
	for _, raw := range []json.RawMessage{c.After, c.Before} {
		var v struct {
			Parent    string         `json:"parent_id"`
			CourseRef string         `json:"course_ref"`
			External  string         `json:"external_id"`
			Fields    map[string]any `json:"fields"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			continue
		}
		course, class, term := controlCourseNative(v.Fields, r.source)
		refs := []string{v.CourseRef, academicField(v.Fields, "course_ref", "courseRef"), academicRef(r.source, course, class, term)}
		if c.Resource == "course" {
			refs = append(refs, v.External)
		}
		// An arbitrary parent (an account exam schedule or folder) proves nothing.
		// A parent which is an archived course must agree with explicit course fields.
		if _, ok := r.courses[[2]string{connection, v.Parent}]; ok {
			refs = append(refs, v.Parent)
		}
		explicit := course != "" || class != "" || term != ""
		var resolved *model.ControlBatchScope
		for _, ref := range refs {
			if ref == "" {
				continue
			}
			explicit = true
			scope := r.courses[[2]string{connection, ref}]
			if scope == nil || (resolved != nil && resolved.CourseRef != scope.CourseRef) {
				return unknown
			}
			resolved = scope
		}
		if explicit {
			if resolved == nil || (hasEvidence && (evidence == nil || evidence.CourseRef != resolved.CourseRef)) {
				return unknown
			}
			if hasEvidence {
				return evidence
			}
			return resolved
		}
	}
	if hasEvidence {
		if evidence != nil {
			return evidence
		}
		return unknown
	}
	if exists && record.connection == connection {
		ref := record.parent
		if record.kind == "course" {
			ref = record.external
		}
		if scope := r.courses[[2]string{connection, ref}]; scope != nil {
			return scope
		}
	}
	return unknown
}
