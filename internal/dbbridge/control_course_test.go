package dbbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

func TestControlCourseFrozenResolution(t *testing.T) {
	a, b := academicRef("mooc", "course", "", "owner-a"), academicRef("mooc", "course", "", "owner-b")
	sa := &model.ControlBatchScope{Kind: "course", ConnectionID: "conn", CourseRef: a, OfferingID: "a", CourseName: "Same"}
	sb := &model.ControlBatchScope{Kind: "course", ConnectionID: "conn", CourseRef: b, OfferingID: "b", CourseName: "Same"}
	resolver := controlCourseResolver{source: "mooc", records: map[string]controlCourseRecord{}, courses: map[[2]string]*model.ControlBatchScope{{"conn", a}: sa, {"conn", b}: sb}, evidence: map[[2]string]*model.ControlBatchScope{}}
	snapshot := func(parent string, fields map[string]any) json.RawMessage {
		return academicJSON(map[string]any{"parent_id": parent, "title": "Same", "fields": fields})
	}
	tests := []struct {
		name          string
		after, before json.RawMessage
		evidence      *model.ControlBatchScope
		want          string
	}{
		{name: "parent", after: snapshot(a, nil), want: a},
		{name: "before deletion", before: snapshot(a, nil), want: a},
		{name: "owner route beats source", after: snapshot(a, map[string]any{"routeCourseId": "course", "routeTermId": "owner-a", "courseId": "course", "termId": "owner-b", "sourceTermId": "owner-b", "activeTermId": "owner-b"}), want: a},
		{name: "source term cannot supply owner", after: snapshot("", map[string]any{"courseId": "course", "sourceTermId": "owner-a", "activeTermId": "owner-a"})},
		{name: "same title cannot supply course", after: snapshot("account-exam-schedule", map[string]any{"course_name": "Same"})},
		{name: "conflicting parent and fields", after: snapshot(a, map[string]any{"courseId": "course", "termId": "owner-b"})},
		{name: "moved current evidence", after: snapshot(a, nil), evidence: sb},
		{name: "explicit unknown after cannot use old before", after: snapshot("", map[string]any{"courseId": "course", "termId": "unknown"}), before: snapshot(a, nil), evidence: sa},
		{name: "structured evidence fallback", after: snapshot("", nil), evidence: sa, want: a},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver.evidence = map[[2]string]*model.ControlBatchScope{}
			if tc.evidence != nil {
				resolver.evidence[[2]string{"conn", "record"}] = tc.evidence
			}
			got := resolver.resolve(model.ControlChange{ConnectionID: "conn", RecordID: "record", After: tc.after, Before: tc.before})
			if got.CourseRef != tc.want || (tc.want == "" && got.Kind != "unassociated") {
				t.Fatalf("scope %+v, want %q", got, tc.want)
			}
		})
	}
	resolver.addCourse("conn", a, sb)
	resolver.addCourse("conn", a, sa)
	if resolver.courses[[2]string{"conn", a}] != nil {
		t.Fatal("ambiguous alias became confirmed again")
	}
}

func controlCourseArchive(t *testing.T, db *DB, owner, source, connection, course, class, term string) string {
	t.Helper()
	ref := academicRef(source, course, class, term)
	fields := map[string]any{"course_id": course, "class_id": class}
	if source == "mooc" {
		fields = map[string]any{"courseId": course, "termId": term}
	}
	insertAcademicFixture(t, db, owner, connection, academicFixture(1, source, "course", ref, "", "Same name", fields))
	must(t, db.transaction(context.Background(), func(tx pgx.Tx) error { return syncAcademicConnection(context.Background(), tx, owner, connection) }))
	return ref
}
func controlCourseEvents(t *testing.T, db *DB, round, owner, source string, changes []model.ControlChange) {
	t.Helper()
	rows := make([][]any, len(changes))
	for i, c := range changes {
		if c.ID == "" {
			c.ID = newID()
		}
		c.Source = source
		rows[i] = []any{c.ID, round, owner, source, academicJSON(c)}
	}
	_, err := db.pool.CopyFrom(context.Background(), pgx.Identifier{"control_events"}, []string{"id", "round_id", "owner_id", "source", "payload"}, pgx.CopyFromRows(rows))
	must(t, err)
}
func controlCourseBatches(t *testing.T, db *DB, round string) []model.ControlBatch {
	t.Helper()
	rows, err := db.pool.Query(context.Background(), `SELECT `+batchCols+` FROM control_batches WHERE round_id=$1 ORDER BY source,created_at,id`, round)
	must(t, err)
	defer rows.Close()
	out := []model.ControlBatch{}
	for rows.Next() {
		b, err := scanBatch(rows)
		must(t, err)
		out = append(out, b)
	}
	must(t, rows.Err())
	return out
}
func TestPostgresControlCourseNoncontiguousIsolationAndFrozenScope(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Control()
	u := fixtureUser(t, db, "course-owner", model.RoleUser)
	v := fixtureUser(t, db, "course-other", model.RoleUser)
	conn := activeAcademicConnection(t, db, u.ID, "chaoxing")
	conn2 := activeAcademicConnection(t, db, u.ID, "chaoxing")
	foreign := activeAcademicConnection(t, db, v.ID, "chaoxing")
	a := controlCourseArchive(t, db, u.ID, "chaoxing", conn, "c", "a", "")
	b := controlCourseArchive(t, db, u.ID, "chaoxing", conn, "c", "b", "")
	controlCourseArchive(t, db, u.ID, "chaoxing", conn2, "c", "a", "")
	controlCourseArchive(t, db, v.ID, "chaoxing", foreign, "c", "a", "")
	round, err := repo.StartRound(ctx, "test")
	must(t, err)
	changes := []model.ControlChange{}
	for i, resource := range []string{"course", "assignment", "exam", "notice", "material", "discussion", "progress", "chapter", "lesson", "discussion_reply_user"} {
		for _, ref := range []string{a, b} {
			after := academicJSON(map[string]any{"parent_id": ref, "title": "Original", "fields": map[string]any{}})
			changes = append(changes, model.ControlChange{ID: fmt.Sprintf("mixed-%02d-%s", i, ref), ConnectionID: conn, Resource: resource, After: after})
		}
	}
	changes = append(changes, model.ControlChange{ConnectionID: conn2, Resource: "notice", After: academicJSON(map[string]any{"parent_id": a})}, model.ControlChange{ConnectionID: conn, Resource: "exam", After: json.RawMessage(`{"parent_id":"account-exam-schedule","title":"Same name","fields":{"course_name":"Same name"}}`)}, model.ControlChange{ConnectionID: conn2, Resource: "exam", After: json.RawMessage(`{"title":"Same name"}`)})
	controlCourseEvents(t, db, round, u.ID, "chaoxing", changes)
	controlCourseEvents(t, db, round, v.ID, "chaoxing", []model.ControlChange{{ConnectionID: foreign, Resource: "notice", After: academicJSON(map[string]any{"parent_id": a})}})
	must(t, repo.Seal(ctx))
	if len(controlCourseBatches(t, db, round)) != 0 {
		t.Fatal("sealed refreshing round")
	}
	must(t, repo.FinishRound(ctx, round, nil, false))
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := repo.Seal(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	batches := controlCourseBatches(t, db, round)
	if len(batches) != 6 {
		t.Fatalf("got %d batches, want6: %+v", len(batches), batches)
	}
	total := 0
	var selected model.ControlBatch
	for _, batch := range batches {
		total += batch.Count
		if batch.Status != "pending" || batch.Scope == nil {
			t.Fatalf("not manually pending: %+v", batch)
		}
		if batch.OwnerID == u.ID && batch.Scope.ConnectionID == conn && batch.Scope.CourseRef == a {
			selected = batch
			if batch.Count != 10 || batch.Scope.OfferingID == "" {
				t.Fatal("resources fragmented", batch)
			}
		}
	}
	if total != len(changes)+1 {
		t.Fatal("events lost", total)
	}
	_, err = db.pool.Exec(ctx, `UPDATE academic_offerings SET name='Renamed' WHERE user_id=$1 AND connection_id=$2`, u.ID, conn)
	must(t, err)
	n, err := repo.Activate(ctx, "", "", selected.ID, false)
	must(t, err)
	if n != 1 {
		t.Fatal("activate", n)
	}
	claimed, err := repo.Claim(ctx, "test")
	must(t, err)
	claimed.Scope = nil
	claimed.Status = "succeeded"
	must(t, repo.SetBatch(ctx, claimed))
	stored, err := repo.GetBatch(ctx, selected.ID)
	must(t, err)
	if !reflect.DeepEqual(stored.Scope, selected.Scope) || stored.Scope.CourseName != "Same name" || !reflect.DeepEqual(stored.Changes, selected.Changes) {
		t.Fatal("scope or event inputs changed after sealing")
	}
}

func TestPostgresControlCourseLargeUnitsAndLegacyLimits(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Control()
	u := fixtureUser(t, db, "course-large", model.RoleUser)
	conn := activeAcademicConnection(t, db, u.ID, "mooc")
	ref := controlCourseArchive(t, db, u.ID, "mooc", conn, "c", "", "owner-term")
	for _, count := range []int{101, 5001, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			round, err := repo.StartRound(ctx, "test")
			must(t, err)
			changes := make([]model.ControlChange, count)
			for i := range changes {
				changes[i] = model.ControlChange{ConnectionID: conn, Resource: "material", After: academicJSON(map[string]any{"parent_id": ref, "fields": map[string]any{"sourceTermId": "reference-term"}})}
			}
			controlCourseEvents(t, db, round, u.ID, "mooc", changes)
			must(t, repo.FinishRound(ctx, round, nil, false))
			must(t, repo.Seal(ctx))
			must(t, repo.Seal(ctx))
			batches := controlCourseBatches(t, db, round)
			if len(batches) != 1 || batches[0].Count != count || len(batches[0].Changes) != count || batches[0].Scope.CourseRef != ref {
				t.Fatalf("course split or lost: count=%d batches=%+v", count, batches)
			}
			if batches[0].Status != "pending" {
				t.Fatal("course within stored-unit budget blocked despite bounded Trigger paging", batches[0].Status)
			}
			n, err := repo.Activate(ctx, round, "", "", false)
			must(t, err)
			if n != 1 {
				t.Fatal("complete course unit did not activate", n)
			}
			var items, unassigned int
			must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_items WHERE batch_id=$1`, batches[0].ID).Scan(&items))
			must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_events WHERE round_id=$1 AND batch_id IS NULL`, round).Scan(&unassigned))
			if items != count || unassigned != 0 {
				t.Fatal("evidence items lost", items, unassigned)
			}
		})
	}
	oversizedRound, e := repo.StartRound(ctx, "oversized-course")
	must(t, e)
	controlCourseEvents(t, db, oversizedRound, u.ID, "mooc", []model.ControlChange{{ConnectionID: conn, Resource: "material", After: academicJSON(map[string]any{"parent_id": ref, "content": strings.Repeat("x", ControlCourseBatchBytes+1)})}})
	must(t, repo.FinishRound(ctx, oversizedRound, nil, false))
	must(t, repo.Seal(ctx))
	oversized := controlCourseBatches(t, db, oversizedRound)
	if len(oversized) != 1 || oversized[0].Status != "blocked" || oversized[0].Error != "COURSE_INPUT_TOO_LARGE" {
		t.Fatal("stored-unit overflow must remain blocked", oversized)
	}
	round, err := repo.StartRound(ctx, "test")
	must(t, err)
	for _, source := range []string{"dws", "ucampus", "jwxt"} {
		changes := make([]model.ControlChange, 101)
		for i := range changes {
			changes[i] = model.ControlChange{Resource: "message", After: json.RawMessage(`{"title":"small"}`)}
		}
		controlCourseEvents(t, db, round, u.ID, source, changes)
	}
	must(t, repo.FinishRound(ctx, round, nil, false))
	must(t, repo.Seal(ctx))
	batches := controlCourseBatches(t, db, round)
	if len(batches) != 6 {
		t.Fatal("legacy chunk count", len(batches))
	}
	for _, b := range batches {
		if b.Scope != nil || (b.Count != 100 && b.Count != 1) || b.Status != "pending" {
			t.Fatal("legacy count behavior changed", b)
		}
	}
	sizeRound, err := repo.StartRound(ctx, "test")
	must(t, err)
	for _, source := range []string{"dws", "ucampus", "jwxt"} {
		controlCourseEvents(t, db, sizeRound, u.ID, source, []model.ControlChange{{After: academicJSON(map[string]any{"content": strings.Repeat("x", 30000)})}, {After: academicJSON(map[string]any{"content": strings.Repeat("x", 30000)})}, {After: academicJSON(map[string]any{"content": strings.Repeat("x", 45000)})}})
	}
	must(t, repo.FinishRound(ctx, sizeRound, nil, false))
	must(t, repo.Seal(ctx))
	batches = controlCourseBatches(t, db, sizeRound)
	if len(batches) != 9 {
		t.Fatal("legacy byte behavior changed", len(batches))
	}
	blocked := 0
	for _, b := range batches {
		if b.Scope != nil || b.Count != 1 {
			t.Fatal("legacy scope/count changed")
		}
		if b.Status == "blocked" {
			blocked++
			if b.Error != "EVENT_TOO_LARGE" {
				t.Fatal("legacy oversized code changed")
			}
		}
	}
	if blocked != 3 {
		t.Fatal("legacy oversized count", blocked)
	}
}
