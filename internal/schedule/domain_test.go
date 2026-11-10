package schedule

import (
	"reflect"
	"testing"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
)

func TestTeachingWeekExpansionAndStrictCalendar(t *testing.T) {
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	c := Calendar{WeekOneMonday: "2026-09-07", Periods: []PeriodTime{{Period: 1, Start: "08:00", End: "08:45"}, {Period: 2, Start: "08:50", End: "09:35"}}}
	r := contract.Record{Kind: contract.Schedule, Title: "数学", Fields: map[string]any{"academic_year": 2026, "term": 1, "entry_type": "class_session", "course_id": "math", "class_id": "class", "weekday": 3, "periods_raw": "1-2节", "weeks_raw": "1-4周(单),8-9周"}}
	b, skipped, e := Expand([]contract.Record{r}, sem, c)
	if e != nil || len(b.Schedules) != 4 || skipped != 0 {
		t.Fatal("expand", len(b.Schedules), e)
	}
	for i, w := range []int{1, 3, 8, 9} {
		s := b.Schedules[i]
		if s.TeachingWeek != w || s.StartTime.In(Location).Weekday() != time.Wednesday || s.EndTime.Sub(s.StartTime) != 95*time.Minute {
			t.Fatal("week/period mismatch", s)
		}
	}
	for _, raw := range []string{"", "第8周", "1-61周", "9-8周", "1周及以后", "1-8(未知)", "0周"} {
		if _, e := Numbers(raw, 60, true); e == nil {
			t.Fatal("partial/invalid weeks accepted", raw)
		}
	}
	for _, date := range []string{"2026-09-08", "bad", "2024-09-02"} {
		bad := c
		bad.WeekOneMonday = date
		if _, _, e := bad.Validate(sem); e == nil {
			t.Fatal("invalid anchor", date)
		}
	}
	bad := c
	bad.Periods = []PeriodTime{{Period: 1, Start: "08:00", End: "08:45"}}
	if _, _, e = Expand([]contract.Record{r}, sem, bad); e == nil {
		t.Fatal("unknown period accepted")
	}
	r.Fields["weeks_raw"] = "1-8周"
	r.Fields["entry_type"] = "practice"
	b, skipped, e = Expand([]contract.Record{r}, sem, c)
	if e != nil || len(b.Schedules) != 0 || skipped != 1 {
		t.Fatal("practice should be explicit skip")
	}
}
func TestTimeStatusBoundaries(t *testing.T) {
	start := time.Date(2026, 9, 7, 8, 0, 0, 0, Location)
	end := start.Add(time.Hour)
	for _, tc := range []struct {
		now  time.Time
		want string
	}{{start.Add(-time.Minute), "NOT_STARTED"}, {start, "IN_PROGRESS"}, {end.Add(-time.Second), "IN_PROGRESS"}, {end, "FINISHED"}, {end.Add(time.Minute), "FINISHED"}} {
		if got := TimeStatus(start, end, tc.now); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
}
func TestTemporaryWeekIsolationAndPermanentHistory(t *testing.T) {
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	start := time.Date(2026, 10, 28, 14, 0, 0, 0, Location)
	base := model.ScheduleFields{TeachingWeek: 8, StartTime: start, EndTime: start.Add(95 * time.Minute), Title: "数学"}
	s := model.Schedule{ID: "week8", Semester: sem, ScheduleFields: base, Source: "TIMETABLE", EventStatus: "INCOMPLETE", Mod: model.ScheduleMod{Base: &base}}
	nextBase := base
	nextBase.TeachingWeek = 9
	nextBase.StartTime = nextBase.StartTime.AddDate(0, 0, 7)
	nextBase.EndTime = nextBase.EndTime.AddDate(0, 0, 7)
	next := s
	next.ID = "week9"
	next.ScheduleFields = nextBase
	next.Mod = model.ScheduleMod{Base: &nextBase}
	thuStart, thuEnd := start.AddDate(0, 0, 1), s.EndTime.AddDate(0, 0, 1)
	permanent := model.ScheduleUpdate{Mode: "PERMANENT", StartTime: &thuStart, EndTime: &thuEnd}
	updated, e := Edit(s, s, permanent, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	week9, e := Edit(next, s, permanent, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	friStart, friEnd := thuStart.AddDate(0, 0, 1), thuEnd.AddDate(0, 0, 1)
	temp := model.ScheduleUpdate{Mode: "TEMPORARY", StartTime: &friStart, EndTime: &friEnd}
	updated, e = Edit(updated, updated, temp, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	rebased, e := Rebase(updated, s, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if rebased.StartTime.Weekday() != time.Friday || week9.StartTime.Weekday() != time.Thursday || len(updated.Mod.History) != 2 || updated.Mod.History[0].OriginalID != "week8" || !updated.Mod.History[0].Before.StartTime.Equal(start) {
		t.Fatal("mod scope/history broken")
	}
	completed := "COMPLETED"
	updated, e = Edit(updated, updated, model.ScheduleUpdate{Mode: "PERMANENT", EventStatus: &completed}, time.Now())
	if e != nil || updated.EventStatus != "COMPLETED" {
		t.Fatal("event state coupled to time", e)
	}
}

func TestAlternatingTeachingWeeksAtSameSlot(t *testing.T) {
	sem := model.Semester{AcademicYear: 2026, Term: 1}
	calendar := Calendar{WeekOneMonday: "2026-09-07", Periods: []PeriodTime{{Period: 1, Start: "08:00", End: "08:45"}, {Period: 2, Start: "08:50", End: "09:35"}}}
	record := func(id, weeks string) contract.Record {
		return contract.Record{Kind: contract.Schedule, ExternalID: id, Title: "同一课程", Fields: map[string]any{"academic_year": 2026, "term": 1, "entry_type": "class_session", "course_id": "same-course", "class_id": "same-class", "weekday": 3, "periods_raw": "1-2节", "weeks_raw": weeks}}
	}
	odd, even := record("odd", "3-17周(单)"), record("even", "2-16周（双周）")
	bundle, _, e := Expand([]contract.Record{odd, even}, sem, calendar)
	if e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	var o, d model.Schedule
	for _, s := range bundle.Schedules {
		counts[s.Mod.WeekPattern]++
		if s.Mod.WeekPattern == "ODD" {
			if s.TeachingWeek%2 != 1 {
				t.Fatal("odd arrangement in even week")
			}
			o = s
		} else {
			if s.TeachingWeek%2 != 0 {
				t.Fatal("even arrangement in odd week")
			}
			d = s
		}
	}
	if counts["ODD"] != 8 || counts["EVEN"] != 8 || len(bundle.Schedules) != 16 {
		t.Fatal(counts)
	}
	if SameWeekPattern(o, d) {
		t.Fatal("alternating arrangements share a modification scope")
	}
	if o.Mod.WeeksRaw != "3-17周(单)" || len(o.Mod.TeachingWeeks) != 8 {
		t.Fatal("source recurrence not retained")
	}
	for _, tc := range []struct {
		raw  string
		want []int
	}{{"3周,7-9周(单),13-17周(单)", []int{3, 7, 9, 13, 15, 17}}, {"2-9周", []int{2, 3, 4, 5, 6, 7, 8, 9}}, {"2-16周(双)", []int{2, 4, 6, 8, 10, 12, 14, 16}}, {"5周,11周", []int{5, 11}}} {
		got, e := Numbers(tc.raw, 60, true)
		if e != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatal(tc.raw, got, e)
		}
	}
}
