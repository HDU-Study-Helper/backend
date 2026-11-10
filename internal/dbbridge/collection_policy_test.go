package dbbridge

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"academic/backend/internal/model"
)

func policyFixture(t *testing.T, db *DB, owner, connection, source, course, class, term string) model.AcademicOffering {
	t.Helper()
	ref := academicRef(source, course, class, term)
	fields := map[string]any{"course_id": course, "class_id": class, "termId": term}
	insertAcademicFixture(t, db, owner, connection, academicFixture(1, source, "course", ref, "", "Same course name", fields))
	_, err := db.Academic().Rebuild(context.Background(), owner, source)
	must(t, err)
	found, err := db.Academic().Offerings(context.Background(), owner, model.AcademicQuery{ConnectionID: connection, CourseRef: ref})
	must(t, err)
	if len(found) != 1 {
		t.Fatalf("fixture offering missing: %+v", found)
	}
	return found[0]
}

func TestCollectionPolicySelectionBounds(t *testing.T) {
	ids, err := collectionSelectionIDs([]string{"b", "a", "b"})
	must(t, err)
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatal(ids)
	}
	for _, invalid := range [][]string{{""}, {" a"}, make([]string, 2001)} {
		if _, err := collectionSelectionIDs(invalid); !errors.Is(err, model.ErrInvalidArgument) {
			t.Fatalf("invalid selection accepted: %v", err)
		}
	}
}

func TestCollectionPolicyExactCourseProof(t *testing.T) {
	cx := academicRef("chaoxing", "course", "class-a", "")
	mooc := academicRef("mooc", "course", "", "owner-route")
	other := academicRef("mooc", "other-course", "", "other-route")
	excluded := map[string]struct{}{cx: {}, mooc: {}}
	cases := []struct {
		name, source, kind, external, parent string
		fields                               map[string]any
		want                                 bool
	}{
		{"course identity", "chaoxing", "course", cx, "", nil, true},
		{"child exact parent", "chaoxing", "assignment", "task", cx, nil, true},
		{"different teaching class", "chaoxing", "assignment", "task", "", map[string]any{"course_id": "course", "class_id": "class-b"}, false},
		{"explicit notice identity", "chaoxing", "notice", "notice", "", map[string]any{"course_id": "course", "class_id": "class-a"}, true},
		{"unproven notice name", "chaoxing", "notice", "notice", "", map[string]any{"course_name": "Same course name"}, false},
		{"account exam remains", "chaoxing", "exam", "exam", "account-exam-schedule", nil, false},
		{"MOOC route blocks active source", "mooc", "material", "file", mooc, map[string]any{"courseId": "course", "termId": "active-content-term", "sourceTermId": "shared-source"}, true},
		{"MOOC explicit route fields", "mooc", "discussion", "topic", "", map[string]any{"routeCourseId": "course", "routeTermId": "owner-route", "courseId": "course", "termId": "active-content-term"}, true},
		{"MOOC shared source isolated", "mooc", "discussion", "topic", other, map[string]any{"routeCourseId": "other-course", "routeTermId": "other-route", "courseId": "other-course", "termId": "other-route", "sourceTermId": "owner-route"}, false},
		{"MOOC only active term no proof", "mooc", "exam", "exam", "", map[string]any{"courseId": "course", "activeTermId": "owner-route"}, false},
		{"exact trusted course ref", "mooc", "notice", "notice", "", map[string]any{"courseRef": mooc}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := academicFixture(1, tc.source, tc.kind, tc.external, tc.parent, "Same course name", tc.fields)
			got, err := collectionRecordExcluded(row, excluded)
			must(t, err)
			if got != tc.want {
				t.Fatalf("excluded=%v want %v", got, tc.want)
			}
		})
	}
}

func TestPostgresCollectionPolicyVersionsIsolationAndRetention(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "policy-owner", model.RoleUser)
	v := fixtureUser(t, db, "policy-other", model.RoleUser)
	c := activeAcademicConnection(t, db, u.ID, "chaoxing")
	foreign := activeAcademicConnection(t, db, v.ID, "chaoxing")
	a := policyFixture(t, db, u.ID, c, "chaoxing", "course", "class-a", "")
	b := policyFixture(t, db, u.ID, c, "chaoxing", "course", "class-b", "")
	f := policyFixture(t, db, v.ID, foreign, "chaoxing", "course", "class-a", "")
	platforms := db.Platforms().(model.CollectionPolicyRepository)
	academic := db.Academic().(model.CollectionPolicyRepository)
	initial, err := platforms.CollectionPolicy(ctx, u.ID)
	must(t, err)
	if initial.Version != 0 || initial.Excluded == nil || len(initial.Excluded) != 0 || initial.UpdatedAt != nil {
		t.Fatalf("initial %+v", initial)
	}
	saved, err := platforms.SaveCollectionPolicy(ctx, u.ID, 0, []string{a.ID, a.ID})
	must(t, err)
	if saved.Version != 1 || len(saved.Excluded) != 1 || saved.Excluded[0].CourseRef != a.CourseRef || saved.UpdatedAt == nil {
		t.Fatalf("saved %+v", saved)
	}
	for _, ids := range [][]string{{a.ID, f.ID}, {b.ID, "unknown"}, {"unknown"}, {f.ID}} {
		_, err := academic.SaveCollectionPolicy(ctx, u.ID, 1, ids)
		if !errors.Is(err, model.ErrInvalidArgument) {
			t.Fatalf("foreign/missing save accepted: %v", err)
		}
		after, err := academic.CollectionPolicy(ctx, u.ID)
		must(t, err)
		if !reflect.DeepEqual(saved, after) {
			t.Fatal("rejected save changed policy")
		}
	}
	if _, err = academic.SaveCollectionPolicy(ctx, u.ID, 0, nil); !errors.Is(err, model.ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	refs, err := academic.ExcludedCourseRefs(ctx, model.DWSScope{UserID: u.ID, ConnectionID: c})
	must(t, err)
	if !reflect.DeepEqual(refs, []string{a.CourseRef}) {
		t.Fatal("exact class refs", refs)
	}
	if _, err = academic.ExcludedCourseRefs(ctx, model.DWSScope{UserID: v.ID, ConnectionID: c}); !errors.Is(err, model.ErrNotFound) {
		t.Fatal("foreign connection exposed", err)
	}
	untouched, err := academic.CollectionPolicy(ctx, v.ID)
	must(t, err)
	if untouched.Version != 0 || len(untouched.Excluded) != 0 {
		t.Fatal("owner policies mixed")
	}
	// Rebuilding projections cannot delete or mutate persistent choices.
	_, err = db.Academic().Rebuild(ctx, u.ID, "")
	must(t, err)
	after, err := academic.CollectionPolicy(ctx, u.ID)
	must(t, err)
	if !reflect.DeepEqual(saved, after) {
		t.Fatal("projection rebuild changed rules")
	}
	// Remove source course; its snapshot remains usable even without an offering.
	_, err = db.pool.Exec(ctx, `DELETE FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind='course' AND external_id=$3`, u.ID, c, a.CourseRef)
	must(t, err)
	_, err = db.Academic().Rebuild(ctx, u.ID, "")
	must(t, err)
	must(t, db.Platforms().Disconnect(ctx, model.DWSScope{UserID: u.ID, ConnectionID: c}))
	after, err = academic.CollectionPolicy(ctx, u.ID)
	must(t, err)
	if !reflect.DeepEqual(saved, after) {
		t.Fatal("disabled/missing rule disappeared")
	}
	saved, err = academic.SaveCollectionPolicy(ctx, u.ID, 1, []string{a.ID})
	must(t, err)
	if saved.Version != 2 || len(saved.Excluded) != 1 {
		t.Fatalf("retained %+v", saved)
	}
	if _, err = academic.SaveCollectionPolicy(ctx, u.ID, 2, []string{a.ID, b.ID}); !errors.Is(err, model.ErrInvalidArgument) {
		t.Fatal("new disabled selection accepted", err)
	}
	empty, err := academic.SaveCollectionPolicy(ctx, u.ID, 2, nil)
	must(t, err)
	if empty.Version != 3 || empty.Excluded == nil || len(empty.Excluded) != 0 {
		t.Fatalf("empty %+v", empty)
	}
	again, err := academic.SaveCollectionPolicy(ctx, u.ID, 3, []string{})
	must(t, err)
	if again.Version != 4 {
		t.Fatal("empty save did not increment")
	}
}

func TestPostgresCollectionPolicyConcurrentVersion(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "policy-race", model.RoleUser)
	c := activeAcademicConnection(t, db, u.ID, "mooc")
	a := policyFixture(t, db, u.ID, c, "mooc", "course", "", "route-term")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := db.SaveCollectionPolicy(ctx, u.ID, 0, []string{a.ID})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, model.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent save success=%d conflict=%d", successes, conflicts)
	}
}

func TestPostgresCollectionPolicyCommitAfterFetch(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "policy-commit", model.RoleUser)
	c := activeAcademicConnection(t, db, u.ID, "mooc")
	other := activeAcademicConnection(t, db, u.ID, "mooc")
	a := policyFixture(t, db, u.ID, c, "mooc", "course", "", "route-term")
	scope := model.DWSScope{UserID: u.ID, ConnectionID: c}
	lease, err := db.Platforms().Acquire(ctx, scope)
	must(t, err)
	defer db.Platforms().Release(ctx, lease)
	_, err = db.Platforms().Load(ctx, lease, "assignment")
	must(t, err)
	// Simulate an upstream fetch that returned before the policy was saved. The
	// barrier forces commit to happen only after the changed policy has committed.
	fetched := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	rows := []model.PlatformRecord{
		academicFixture(2, "mooc", "assignment", "blocked-task", a.CourseRef, "Blocked task", map[string]any{"courseId": "course", "termId": "active-content-term", "sourceTermId": "shared-term"}),
		academicFixture(3, "mooc", "assignment", "allowed-task", academicRef("mooc", "other", "", "other-route"), "Allowed task", map[string]any{"courseId": "other", "termId": "other-route", "sourceTermId": "shared-term"}),
	}
	go func() {
		close(fetched)
		<-resume
		done <- db.Platforms().Commit(ctx, lease, "assignment", rows, []byte("new-session"), []byte("advanced-cursor"))
	}()
	<-fetched
	_, err = db.SaveCollectionPolicy(ctx, u.ID, 0, []string{a.ID})
	must(t, err)
	close(resume)
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("commit stalled")
	}
	var raw, items, events int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind='assignment'`, u.ID, c).Scan(&raw))
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM academic_items WHERE user_id=$1 AND connection_id=$2 AND kind='assignment'`, u.ID, c).Scan(&items))
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM control_events WHERE owner_id=$1 AND payload->>'resource'='assignment'`, u.ID).Scan(&events))
	if raw != 1 || items != 1 || events != 1 {
		t.Fatalf("excluded writes escaped: raw=%d items=%d events=%d", raw, items, events)
	}
	var external string
	must(t, db.pool.QueryRow(ctx, `SELECT external_id FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind='assignment'`, u.ID, c).Scan(&external))
	if external != "allowed-task" {
		t.Fatal("wrong record survived", external)
	}
	state, err := db.Platforms().Load(ctx, lease, "assignment")
	must(t, err)
	if string(state.Session) != "new-session" || string(state.Cursor) != "advanced-cursor" {
		t.Fatal("filter prevented progress")
	}
	// A course update itself is also suppressed, leaving its old stored snapshot.
	must(t, db.Platforms().Commit(ctx, lease, "course", []model.PlatformRecord{academicFixture(4, "mooc", "course", a.CourseRef, "", "Must not replace old name", map[string]any{"courseId": "course", "termId": "route-term"})}, nil, nil))
	var name string
	must(t, db.pool.QueryRow(ctx, `SELECT content->>'title' FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND kind='course' AND external_id=$3`, u.ID, c, a.CourseRef).Scan(&name))
	if name != "Same course name" {
		t.Fatal("excluded course overwritten", name)
	}
	// Same native route in a different connection is independent.
	otherLease, err := db.Platforms().Acquire(ctx, model.DWSScope{UserID: u.ID, ConnectionID: other})
	must(t, err)
	defer db.Platforms().Release(ctx, otherLease)
	must(t, db.Platforms().Commit(ctx, otherLease, "assignment", rows[:1], nil, nil))
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM platform_records WHERE user_id=$1 AND connection_id=$2 AND external_id='blocked-task'`, u.ID, other).Scan(&raw))
	if raw != 1 {
		t.Fatal("connection scope leaked")
	}
}
