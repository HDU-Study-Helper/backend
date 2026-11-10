package dbbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/bridge"
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"academic/backend/internal/platform"
	"academic/backend/internal/schedule"
)

type timetableAdapter struct {
	contract.Adapter
	records  []contract.Record
	failPage int
	reads    int
	pageSize int
}

func (*timetableAdapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "jwxt", PasswordLogin: true, ManualSyncOnly: true, Resources: []contract.Resource{contract.Schedule}}
}
func (*timetableAdapter) Login(context.Context, contract.Scope, string, string) (contract.AuthResult, error) {
	return contract.AuthResult{Status: "authorized", Session: []byte("fixture-session"), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (a *timetableAdapter) SyncTerm(_ context.Context, _ contract.Scope, _ contract.Session, _ contract.Resource, _ int, _ int, cursor string) (contract.Page, error) {
	a.reads++
	if a.failPage > 0 && a.reads == a.failPage {
		return contract.Page{}, contract.ErrUpstream
	}
	start := 0
	if cursor != "" {
		if cursor != "next" {
			return contract.Page{}, contract.ErrParse
		}
		start = a.pageSize
	}
	end := len(a.records)
	more := false
	next := ""
	if a.pageSize > 0 && start == 0 && len(a.records) > a.pageSize {
		end = a.pageSize
		more = true
		next = "next"
	}
	return contract.Page{Records: a.records[start:end], HasMore: more, NextCursor: next}, nil
}
func timetableRecord(id, title, location string, day int, sem model.Semester) contract.Record {
	parent, _ := json.Marshal([]string{fmtYear(sem.AcademicYear), fmtYear(sem.Term)})
	return contract.Record{Kind: contract.Schedule, ExternalID: id, ParentID: string(parent), Title: title, Fields: map[string]any{"academic_year": sem.AcademicYear, "term": sem.Term, "entry_type": "class_session", "course_id": id, "class_id": "class-" + id, "weekday": day, "periods_raw": "1-2节", "weeks_raw": "8-9周", "teacher": "张老师", "location": location}}
}
func fmtYear(n int) string { b, _ := json.Marshal(n); return string(b) }
func fixtureCalendar() schedule.Calendar {
	return schedule.Calendar{WeekOneMonday: "2026-09-07", Periods: []schedule.PeriodTime{{Period: 1, Start: "08:00", End: "08:45"}, {Period: 2, Start: "08:50", End: "09:35"}}}
}
func weekSchedules(t *testing.T, db *DB, owner string, sem model.Semester, week int) []model.Schedule {
	t.Helper()
	out, e := db.Schedules().List(context.Background(), owner, model.ScheduleQuery{Semester: sem, TeachingWeek: week, Limit: 1000})
	must(t, e)
	return out
}
func findTitle(t *testing.T, items []model.Schedule, title string) model.Schedule {
	t.Helper()
	for _, s := range items {
		if s.Title == title {
			return s
		}
	}
	t.Fatal("missing schedule", title)
	return model.Schedule{}
}
func setupTimetable(t *testing.T, db *DB, owner string) (*platform.Service, *timetableAdapter, model.DWSScope) {
	t.Helper()
	v, e := credential.New([]byte(strings.Repeat("k", 32)))
	must(t, e)
	a := &timetableAdapter{}
	s, e := platform.New(db.Platforms(), v, []contract.Adapter{a})
	must(t, e)
	c, e := s.LoginPassword(context.Background(), owner, "jwxt", "fixture", "fixture")
	must(t, e)
	return s, a, model.DWSScope{UserID: owner, ConnectionID: c.ID}
}
func TestPostgresTimetableSchedulesLifecycle(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "schedules", model.RoleUser)
	other := fixtureUser(t, db, "scheduleother", model.RoleUser)
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	calendar := fixtureCalendar()
	s, a, scope := setupTimetable(t, db, u.ID)
	a.records = []contract.Record{timetableRecord("math", "数学", "A101", 3, sem), timetableRecord("physics", "物理", "B101", 2, sem)}
	a.pageSize = 1
	otherService, otherAdapter, otherScope := setupTimetable(t, db, other.ID)
	otherAdapter.records = []contract.Record{timetableRecord("other", "别人课程", "D101", 1, sem)}
	_, e := otherService.SyncTimetable(ctx, otherScope, sem, calendar)
	must(t, e)
	result, e := s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	if result.Generated != 4 || a.reads != 2 {
		t.Fatal("full snapshot not expanded", result, a.reads)
	}
	week8 := weekSchedules(t, db, u.ID, sem, 8)
	week9 := weekSchedules(t, db, u.ID, sem, 9)
	if len(week8) != 2 || len(week9) != 2 {
		t.Fatal("multi-week/multiple occurrence query failed")
	}
	math := findTitle(t, week8, "数学")
	completed := "COMPLETED"
	out, e := db.Schedules().Update(ctx, u.ID, math.ID, model.ScheduleUpdate{EventStatus: &completed})
	must(t, e)
	if out.EventStatus != "COMPLETED" {
		t.Fatal("completion update failed")
	}
	a.reads = 0
	_, e = s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	again := findTitle(t, weekSchedules(t, db, u.ID, sem, 8), "数学")
	if again.ID != math.ID || again.EventStatus != "COMPLETED" {
		t.Fatal("repeat sync lost id/completion")
	}
	// Manual and Agent events share the week but are outside replacement scope.
	manual := model.Schedule{Semester: sem, ScheduleFields: math.ScheduleFields, Source: "USER"}
	manual.Title = "个人事项"
	manual, e = db.Schedules().Create(ctx, u.ID, manual)
	must(t, e)
	port := schedule.New(db.Schedules(), nil)
	payload, _ := json.Marshal(schedule.CreateInput{Semester: sem, ScheduleFields: model.ScheduleFields{TeachingWeek: 8, StartTime: math.StartTime, EndTime: math.EndTime, Title: "Agent事项"}})
	reply, e := port.Call(ctx, bridge.Request{Version: bridge.Version, OwnerID: u.ID, Operation: "schedule.create", Payload: payload})
	must(t, e)
	var agentEvent model.Schedule
	must(t, json.Unmarshal(reply.Payload, &agentEvent))
	if agentEvent.Source != "AGENT" {
		t.Fatal("bridge source wrong")
	}
	// A permanent shift applies from week 8 forward. A later temporary shift wins only in week 8.
	thuStart, thuEnd := math.StartTime.AddDate(0, 0, 1), math.EndTime.AddDate(0, 0, 1)
	_, e = db.Schedules().Update(ctx, u.ID, math.ID, model.ScheduleUpdate{Mode: "PERMANENT", StartTime: &thuStart, EndTime: &thuEnd})
	must(t, e)
	friStart, friEnd := math.StartTime.AddDate(0, 0, 2), math.EndTime.AddDate(0, 0, 2)
	_, e = db.Schedules().Update(ctx, u.ID, math.ID, model.ScheduleUpdate{Mode: "TEMPORARY", StartTime: &friStart, EndTime: &friEnd})
	must(t, e)
	// Upstream room change and cancellation refresh only timetable rows.
	a.records = []contract.Record{timetableRecord("math", "数学", "A202", 3, sem)}
	a.reads = 0
	result, e = s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	week8 = weekSchedules(t, db, u.ID, sem, 8)
	week9 = weekSchedules(t, db, u.ID, sem, 9)
	if len(week8) != 3 || len(week9) != 1 {
		t.Fatal("replacement damaged USER/AGENT or retained cancelled class", len(week8), len(week9))
	}
	math = findTitle(t, week8, "数学")
	if math.StartTime.In(schedule.Location).Weekday() != time.Friday || week9[0].StartTime.In(schedule.Location).Weekday() != time.Thursday || !strings.Contains(math.Details, "A202") || math.EventStatus != "COMPLETED" {
		t.Fatal("override rebase/week scope failed", math, week9)
	}
	if len(math.Mod.History) != 3 || math.Mod.History[0].Mode != "PERMANENT" || math.Mod.History[1].Mode != "TEMPORARY" || math.Mod.History[2].Mode != "SYNC" {
		t.Fatal("history not traceable", math.Mod.History)
	}
	for _, id := range []string{manual.ID, agentEvent.ID} {
		if _, e = db.Schedules().Get(ctx, u.ID, id); e != nil {
			t.Fatal("non timetable event lost", e)
		}
	}
	// Crossing a user's scope must not permit get/update/delete.
	if _, e = db.Schedules().Get(ctx, other.ID, math.ID); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross-user get", e)
	}
	if _, e = db.Schedules().Update(ctx, other.ID, math.ID, model.ScheduleUpdate{EventStatus: &completed}); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross-user update", e)
	}
	if e = db.Schedules().Delete(ctx, other.ID, math.ID); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross-user delete", e)
	}
	if items := weekSchedules(t, db, other.ID, sem, 8); len(items) != 1 || items[0].Title != "别人课程" {
		t.Fatal("cross-user list")
	}
	// Newly observed future weeks inherit permanent adjustments only.
	a.records[0].Fields["weeks_raw"] = "8-10周"
	_, e = s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	added := weekSchedules(t, db, u.ID, sem, 10)
	if len(added) != 1 || added[0].StartTime.In(schedule.Location).Weekday() != time.Thursday || added[0].Mod.Temporary != nil || len(added[0].Mod.History) == 0 {
		t.Fatal("new future week lost permanent policy or inherited temporary change")
	}
	// A second semester has independent week numbers and cannot retire term 1.
	otherSem := model.Semester{AcademicYear: 2026, Term: 2}
	otherCal := calendar
	otherCal.WeekOneMonday = "2027-02-22"
	a.records = []contract.Record{timetableRecord("math", "下学期数学", "C101", 1, otherSem)}
	_, e = s.SyncTimetable(ctx, scope, otherSem, otherCal)
	must(t, e)
	if len(weekSchedules(t, db, u.ID, sem, 8)) != 3 || len(weekSchedules(t, db, u.ID, otherSem, 8)) != 1 {
		t.Fatal("semester isolation failed")
	}
	// Permanent and temporary records stay queryable by time and independent status.
	items, e := db.Schedules().List(ctx, u.ID, model.ScheduleQuery{From: math.StartTime, To: math.EndTime})
	must(t, e)
	if len(items) == 0 {
		t.Fatal("range query empty")
	}
	must(t, db.Schedules().Delete(ctx, u.ID, manual.ID))
	if _, e = db.Schedules().Get(ctx, u.ID, manual.ID); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("manual delete failed")
	}
	must(t, db.Schedules().Delete(ctx, u.ID, math.ID))
	deleted, e := db.Schedules().Get(ctx, u.ID, math.ID)
	must(t, e)
	if deleted.Active || !deleted.Mod.Deleted {
		t.Fatal("timetable deletion not retained")
	}
	a.records = []contract.Record{timetableRecord("math", "数学", "A202", 3, sem)}
	_, e = s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	if len(weekSchedules(t, db, u.ID, sem, 8)) != 1 {
		t.Fatal("deleted occurrence resurrected")
	}
}

func TestPostgresTimetableFailureAndRollback(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "schedulefailure", model.RoleUser)
	s, a, scope := setupTimetable(t, db, u.ID)
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	calendar := fixtureCalendar()
	a.records = []contract.Record{timetableRecord("math", "数学", "A101", 3, sem)}
	_, e := s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	original := weekSchedules(t, db, u.ID, sem, 8)[0]
	assertOld := func() {
		t.Helper()
		items := weekSchedules(t, db, u.ID, sem, 8)
		if len(items) != 1 || items[0].ID != original.ID || items[0].Title != "数学" {
			t.Fatal("old schedules destroyed", items)
		}
		archived, e := db.Platforms().Records(ctx, scope, "schedule", 0, 100)
		must(t, e)
		if len(archived) != 1 || !strings.Contains(string(archived[0].Content), "数学") {
			t.Fatal("archive partial commit")
		}
	}
	// Fail after a successful first page: zero replacement or archive writes.
	a.records = append(a.records, timetableRecord("physics", "物理", "B101", 2, sem))
	a.pageSize = 1
	a.reads = 0
	a.failPage = 2
	if _, e = s.SyncTimetable(ctx, scope, sem, calendar); !errors.Is(e, contract.ErrUpstream) {
		t.Fatal("upstream failure swallowed", e)
	}
	assertOld()
	a.failPage = 0
	a.pageSize = 0
	a.records = a.records[:1]
	a.records[0].Fields["weeks_raw"] = "未知周次"
	if _, e = s.SyncTimetable(ctx, scope, sem, calendar); e == nil {
		t.Fatal("invalid weeks swallowed")
	}
	assertOld()
	a.records = []contract.Record{timetableRecord("math", "FORCE_ROLLBACK", "A101", 3, sem)}
	_, e = db.pool.Exec(ctx, `CREATE FUNCTION reject_schedule_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.title='FORCE_ROLLBACK' THEN RAISE EXCEPTION 'fixture rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_schedule_fixture BEFORE INSERT OR UPDATE ON schedules FOR EACH ROW EXECUTE FUNCTION reject_schedule_fixture();`)
	must(t, e)
	if _, e = s.SyncTimetable(ctx, scope, sem, calendar); e == nil {
		t.Fatal("transaction write failure swallowed")
	}
	assertOld()
	// Validate configuration before upstream access.
	reads := a.reads
	bad := calendar
	bad.WeekOneMonday = "2026-09-08"
	if _, e = s.SyncTimetable(ctx, scope, sem, bad); e == nil || a.reads != reads {
		t.Fatal("invalid anchor read upstream")
	}
}

func TestPostgresScheduleHTTP(t *testing.T) {
	db := testDB(t)
	u := fixtureUser(t, db, "schedulehttp", model.RoleUser)
	svc := schedule.New(db.Schedules(), nil)
	handler := svc.Routes()
	request := func(method, path, body, owner string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if owner != "" {
			r = r.WithContext(httpkit.WithUser(r.Context(), model.User{ID: owner}))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"academic_year":2026,"term":1,"teaching_week":8,"start_time":"2026-10-28T14:00:00+08:00","end_time":"2026-10-28T15:35:00+08:00","title":"用户日程","details":"测试"}`
	if w := request(http.MethodPost, "/", body, ""); w.Code != 401 {
		t.Fatal("unauthenticated create")
	}
	w := request(http.MethodPost, "/", body, u.ID)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var s model.Schedule
	must(t, json.Unmarshal(w.Body.Bytes(), &s))
	if s.Source != "USER" || s.EventStatus != "INCOMPLETE" || s.TimeStatus == "" {
		t.Fatal("missing query fields")
	}
	w = request(http.MethodGet, "/?academic_year=2026&term=1&teaching_week=8", "", u.ID)
	if w.Code != 200 || !strings.Contains(w.Body.String(), s.ID) {
		t.Fatal("HTTP week query failed", w.Code, w.Body.String())
	}
	w = request(http.MethodPatch, "/"+s.ID, `{"event_status":"COMPLETED"}`, u.ID)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "COMPLETED") {
		t.Fatal("HTTP status failed", w.Code)
	}
	if w = request(http.MethodDelete, "/"+s.ID, "", u.ID); w.Code != 204 {
		t.Fatal("HTTP delete", w.Code)
	}
}

func TestPostgresTimetableHTTPAutoRefresh(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "timetablehttp", model.RoleUser)
	s, a, scope := setupTimetable(t, db, u.ID)
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	calendar := fixtureCalendar()
	a.records = []contract.Record{timetableRecord("math", "数学", "A101", 3, sem)}
	request := func(body string, owner string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/connections/"+scope.ConnectionID+"/sync", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(httpkit.WithUser(ctx, model.User{ID: owner}))
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, r)
		return w
	}
	if w := request(`{"resource":"schedule","academic_year":2026,"term":1}`, u.ID); w.Code != 400 || a.reads != 0 {
		t.Fatal("calendar required before upstream", w.Code)
	}
	raw, _ := json.Marshal(map[string]any{"resource": "schedule", "academic_year": 2026, "term": 1, "calendar": calendar})
	w := request(string(raw), u.ID)
	if w.Code != 200 {
		t.Fatal("manual endpoint", w.Code, w.Body.String())
	}
	var result model.TimetableResult
	must(t, json.Unmarshal(w.Body.Bytes(), &result))
	if result.Generated != 2 || len(weekSchedules(t, db, u.ID, sem, 8)) != 1 {
		t.Fatal("HTTP sync did not refresh schedules", result)
	}
	// A changed upstream time uses the same identity for a single weekly arrangement.
	original := weekSchedules(t, db, u.ID, sem, 8)[0]
	a.records[0].Fields["weekday"] = 4
	w = request(string(raw), u.ID)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	updated := weekSchedules(t, db, u.ID, sem, 8)[0]
	if updated.ID != original.ID || updated.StartTime.In(schedule.Location).Weekday() != time.Thursday || len(updated.Mod.History) != 1 {
		t.Fatal("upstream time change not rebased")
	}
	// An explicitly valid empty complete snapshot retires only timetable schedules.
	manual, e := db.Schedules().Create(ctx, u.ID, model.Schedule{Semester: sem, ScheduleFields: original.ScheduleFields, Source: "USER"})
	must(t, e)
	a.records = []contract.Record{}
	w = request(string(raw), u.ID)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	items := weekSchedules(t, db, u.ID, sem, 8)
	if len(items) != 1 || items[0].ID != manual.ID {
		t.Fatal("complete empty snapshot not scoped to timetable")
	}
}

func TestPostgresTimetableAmbiguousIdentityConflict(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "identityconflict", model.RoleUser)
	s, a, scope := setupTimetable(t, db, u.ID)
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	calendar := fixtureCalendar()
	a.records = []contract.Record{timetableRecord("math", "数学", "A101", 3, sem)}
	_, e := s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	original := weekSchedules(t, db, u.ID, sem, 8)[0]
	title := "临时调整"
	_, e = db.Schedules().Update(ctx, u.ID, original.ID, model.ScheduleUpdate{Mode: "TEMPORARY", Title: &title})
	must(t, e)
	second := timetableRecord("math", "数学", "A102", 5, sem)
	second.ExternalID = "second-arrangement"
	a.records = append(a.records, second)
	result, e := s.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	if len(result.RetiredModifications) != 1 || result.RetiredModifications[0] != original.ID || len(weekSchedules(t, db, u.ID, sem, 8)) != 2 {
		t.Fatal("ambiguous source change not explicit", result)
	}
	retired, e := db.Schedules().Get(ctx, u.ID, original.ID)
	must(t, e)
	if retired.Active || retired.Mod.Temporary == nil || len(retired.Mod.History) != 2 {
		t.Fatal("conflicting local adjustment history discarded")
	}
}

func TestPostgresAlternatingWeeksDoNotSharePermanentChanges(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "alternatingweeks", model.RoleUser)
	service, a, scope := setupTimetable(t, db, u.ID)
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	calendar := fixtureCalendar()
	odd := timetableRecord("same-course", "单双周课程", "单周教室", 3, sem)
	odd.ExternalID = "odd-arrangement"
	odd.Fields["weeks_raw"] = "1-8周(单)"
	even := timetableRecord("same-course", "单双周课程", "双周教室", 3, sem)
	even.ExternalID = "even-arrangement"
	even.Fields["weeks_raw"] = "1-8周(双)"
	a.records = []contract.Record{odd, even}
	result, e := service.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	if result.Generated != 8 {
		t.Fatal("alternating weeks duplicated", result)
	}
	first := weekSchedules(t, db, u.ID, sem, 1)[0]
	second := weekSchedules(t, db, u.ID, sem, 2)[0]
	if first.Mod.WeekPattern != "ODD" || second.Mod.WeekPattern != "EVEN" || first.SeriesKey != second.SeriesKey {
		t.Fatal("fixture must exercise alternating arrangements at same source slot")
	}
	titleOdd, titleEven := "单周永久调整", "双周永久调整"
	_, e = db.Schedules().Update(ctx, u.ID, first.ID, model.ScheduleUpdate{Mode: "PERMANENT", Title: &titleOdd})
	must(t, e)
	if got := weekSchedules(t, db, u.ID, sem, 2)[0]; got.Title != second.Title || got.Mod.Permanent != nil {
		t.Fatal("single-week change leaked into double week")
	}
	_, e = db.Schedules().Update(ctx, u.ID, second.ID, model.ScheduleUpdate{Mode: "PERMANENT", Title: &titleEven})
	must(t, e)
	a.records[0].Fields["weeks_raw"] = "1-10周(单)"
	a.records[1].Fields["weeks_raw"] = "1-10周(双)"
	_, e = service.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	for week := 1; week <= 10; week++ {
		items := weekSchedules(t, db, u.ID, sem, week)
		if len(items) != 1 {
			t.Fatal("alternating slot has duplicate/missing occurrence", week, len(items))
		}
		want := titleOdd
		if week%2 == 0 {
			want = titleEven
		}
		if items[0].Title != want {
			t.Fatal("permanent inheritance crossed parity", week, items[0].Title, want)
		}
	}
	third := weekSchedules(t, db, u.ID, sem, 3)[0]
	temporary := "第3周临时调整"
	_, e = db.Schedules().Update(ctx, u.ID, third.ID, model.ScheduleUpdate{Mode: "TEMPORARY", Title: &temporary})
	must(t, e)
	_, e = service.SyncTimetable(ctx, scope, sem, calendar)
	must(t, e)
	if weekSchedules(t, db, u.ID, sem, 3)[0].Title != temporary || weekSchedules(t, db, u.ID, sem, 5)[0].Title != titleOdd || weekSchedules(t, db, u.ID, sem, 4)[0].Title != titleEven {
		t.Fatal("temporary adjustment leaked across weeks")
	}
}
