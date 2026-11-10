package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/model"
)

type PeriodTime struct {
	Period int    `json:"period"`
	Start  string `json:"start"`
	End    string `json:"end"`
}
type Calendar struct {
	WeekOneMonday string       `json:"week_one_monday"`
	Periods       []PeriodTime `json:"periods"`
}

var numberRange = regexp.MustCompile(`^(\d{1,2})(?:-(\d{1,2}))?(?:\((单|双)\))?$`)

// Numbers accepts explicit ranges/lists and odd/even teaching-week qualifiers.
// Unknown syntax fails closed rather than producing a partial replacement.
func Numbers(raw string, max int, parity bool) ([]int, error) {
	raw = strings.NewReplacer("周", "", "节", "", "（", "(", "）", ")", "，", ",", "、", ",", " ", "", "\t", "", "－", "-").Replace(raw)
	if raw == "" {
		return nil, model.ErrInvalidArgument
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		m := numberRange.FindStringSubmatch(part)
		if m == nil || (!parity && m[3] != "") {
			return nil, model.ErrInvalidArgument
		}
		a, _ := strconv.Atoi(m[1])
		b := a
		if m[2] != "" {
			b, _ = strconv.Atoi(m[2])
		}
		if a < 1 || b < a || b > max {
			return nil, model.ErrInvalidArgument
		}
		for n := a; n <= b; n++ {
			if m[3] == "单" && n%2 == 0 || m[3] == "双" && n%2 != 0 {
				continue
			}
			seen[n] = true
		}
	}
	out := []int{}
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	if len(out) == 0 {
		return nil, model.ErrInvalidArgument
	}
	return out, nil
}
func (c Calendar) Validate(sem model.Semester) (time.Time, map[int][2]int, error) {
	monday, e := time.ParseInLocation("2006-01-02", c.WeekOneMonday, Location)
	periods := map[int][2]int{}
	if !ValidSemester(sem) || e != nil || monday.Weekday() != time.Monday || (monday.Year() != sem.AcademicYear && monday.Year() != sem.AcademicYear+1) || len(c.Periods) == 0 || len(c.Periods) > 30 {
		return monday, nil, model.ErrInvalidArgument
	}
	last := -1
	sorted := append([]PeriodTime{}, c.Periods...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Period < sorted[j].Period })
	for _, p := range sorted {
		a, ea := time.Parse("15:04", p.Start)
		b, eb := time.Parse("15:04", p.End)
		start, end := a.Hour()*60+a.Minute(), b.Hour()*60+b.Minute()
		if p.Period < 1 || p.Period > 30 || ea != nil || eb != nil || len(p.Start) != 5 || len(p.End) != 5 || start >= end || start < last {
			return monday, nil, model.ErrInvalidArgument
		}
		if _, ok := periods[p.Period]; ok {
			return monday, nil, model.ErrInvalidArgument
		}
		periods[p.Period] = [2]int{start, end}
		last = end
	}
	return monday, periods, nil
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type sessionFields struct {
	AcademicYear int    `json:"academic_year"`
	Term         int    `json:"term"`
	EntryType    string `json:"entry_type"`
	CourseID     string `json:"course_id"`
	ClassID      string `json:"class_id"`
	Weekday      int    `json:"weekday"`
	Periods      string `json:"periods_raw"`
	Weeks        string `json:"weeks_raw"`
	Teacher      string `json:"teacher"`
	Location     string `json:"location"`
}

func Expand(records []contract.Record, sem model.Semester, c Calendar) (model.TimetableRefresh, int, error) {
	result := model.TimetableRefresh{Semester: sem, Schedules: []model.Schedule{}}
	monday, periodTimes, e := c.Validate(sem)
	if e != nil {
		return result, 0, e
	}
	fields := make([]sessionFields, len(records))
	groups := map[string]int{}
	groupKeys := make([]string, len(records))
	skipped := 0
	for i, r := range records {
		b, e := json.Marshal(r.Fields)
		if e != nil || json.Unmarshal(b, &fields[i]) != nil || r.Kind != contract.Schedule {
			return result, 0, contract.ErrParse
		}
		f := fields[i]
		if f.AcademicYear != sem.AcademicYear || f.Term != sem.Term {
			return result, 0, contract.ErrParse
		}
		if f.EntryType == "practice" {
			skipped++
			continue
		}
		if f.EntryType != "class_session" || f.Weekday < 1 || f.Weekday > 7 || strings.TrimSpace(r.Title) == "" {
			return result, 0, contract.ErrParse
		}
		identity := []string{f.CourseID, f.ClassID}
		if f.CourseID == "" && f.ClassID == "" {
			identity = append(identity, r.Title, f.Teacher)
		}
		groupKeys[i] = digest(identity)
		groups[groupKeys[i]]++
	}
	seen := map[string]bool{}
	for i, r := range records {
		f := fields[i]
		if f.EntryType == "practice" {
			continue
		}
		weeks, err := Numbers(f.Weeks, 60, true)
		if err != nil {
			return result, 0, contract.ErrParse
		}
		periods, err := Numbers(f.Periods, 30, false)
		if err != nil {
			return result, 0, contract.ErrParse
		}
		// Non-contiguous periods are separate concrete occurrences.
		runs := [][2]int{}
		for _, p := range periods {
			if _, ok := periodTimes[p]; !ok {
				return result, 0, model.ErrInvalidArgument
			}
			if len(runs) == 0 || p > runs[len(runs)-1][1]+1 {
				runs = append(runs, [2]int{p, p})
			} else {
				runs[len(runs)-1][1] = p
			}
		}
		for _, run := range runs {
			series := groupKeys[i]
			if groups[series] > 1 || len(runs) > 1 {
				series = digest([]any{series, f.Weekday, run})
			}
			for _, week := range weeks {
				origin := series + ":" + strconv.Itoa(week)
				if seen[origin] {
					return result, 0, contract.ErrParse
				}
				seen[origin] = true
				date := monday.AddDate(0, 0, (week-1)*7+f.Weekday-1)
				sf := model.ScheduleFields{TeachingWeek: week, StartTime: date.Add(time.Duration(periodTimes[run[0]][0]) * time.Minute).UTC(), EndTime: date.Add(time.Duration(periodTimes[run[1]][1]) * time.Minute).UTC(), Title: r.Title, Details: strings.TrimSpace("教师：" + f.Teacher + "；地点：" + f.Location)}
				if !ValidFields(sf) {
					return result, 0, contract.ErrParse
				}
				base := sf
				result.Schedules = append(result.Schedules, model.Schedule{Semester: sem, ScheduleFields: sf, Source: "TIMETABLE", EventStatus: "INCOMPLETE", SeriesKey: series, OriginKey: origin, Active: true, Mod: model.ScheduleMod{Base: &base, WeeksRaw: f.Weeks, TeachingWeeks: append([]int{}, weeks...), WeekPattern: WeekPattern(weeks), History: []model.ScheduleChange{}}})
				if len(result.Schedules) > 20000 {
					return result, 0, contract.ErrParse
				}
			}
		}
	}
	return result, skipped, nil
}

// WeekPattern is based on teaching-week numbers, never ISO/calendar week numbers.
func WeekPattern(weeks []int) string {
	odd, even := false, false
	for _, w := range weeks {
		if w%2 == 0 {
			even = true
		} else {
			odd = true
		}
	}
	if odd && !even {
		return "ODD"
	}
	if even && !odd {
		return "EVEN"
	}
	return "ALL"
}

// SameWeekPattern prevents a permanent change or inherited policy crossing from
// a single-week arrangement into its alternating double-week arrangement.
func SameWeekPattern(a, b model.Schedule) bool {
	pa, pb := a.Mod.WeekPattern, b.Mod.WeekPattern
	if pa == "" && len(a.Mod.TeachingWeeks) > 0 {
		pa = WeekPattern(a.Mod.TeachingWeeks)
	}
	if pb == "" && len(b.Mod.TeachingWeeks) > 0 {
		pb = WeekPattern(b.Mod.TeachingWeeks)
	}
	if pa == "" {
		pa = "ALL"
	}
	if pb == "" {
		pb = "ALL"
	}
	return pa == pb
}
