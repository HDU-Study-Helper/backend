package dbbridge

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/model"
)

var (
	academicLocation = func() *time.Location {
		location, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			return time.FixedZone("Asia/Shanghai", 8*60*60)
		}
		return location
	}()
	academicFullDate       = regexp.MustCompile(`^([0-9]{4})[-/.年]([0-9]{1,2})[-/.月]([0-9]{1,2})日?(?:[T[:space:]]+([0-9]{1,2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]{1,9}))?)?)?$`)
	academicMonthDay       = regexp.MustCompile(`^([0-9]{1,2})[-/月]([0-9]{1,2})日?[[:space:]]+([0-9]{1,2}):([0-9]{2})$`)
	academicClock          = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]{1,9}))?)?$`)
	academicRangeSeparator = regexp.MustCompile(`[~～至—–－]|[[:space:]]+-[[:space:]]+`)
)

// parseAcademicTime retains source evidence even when it cannot establish a year
// or interpret the value. Date-only evidence populates Date without inventing
// an instant; collection time and the current year are never used as substitutes.
func parseAcademicTime(raw, field string) model.AcademicTime {
	result := model.AcademicTime{Raw: raw, SourceField: field, Precision: "unknown", State: "partial"}
	text := strings.TrimSpace(raw)
	if text == "" {
		result.State = "missing"
		return result
	}
	if parsed, precision, ok := academicExplicitTime(text); ok {
		result.Precision, result.State = precision, "known"
		if precision == "day" {
			result.Date = parsed.Format("2006-01-02")
		} else {
			parsed = parsed.UTC()
			result.Value = &parsed
		}
		return result
	}
	if epoch, err := strconv.ParseInt(text, 10, 64); err == nil {
		// Nine/ten-digit seconds and twelve/thirteen-digit milliseconds cover
		// ordinary platform timestamps without interpreting compact YYYYMMDD
		// dates, small counters, or common zero/-1 sentinels as epochs.
		var parsed time.Time
		switch {
		case epoch >= 100000000 && epoch < 10000000000:
			parsed = time.Unix(epoch, 0).UTC()
		case epoch >= 100000000000 && epoch < 10000000000000:
			parsed = time.UnixMilli(epoch).UTC()
		}
		if !parsed.IsZero() {
			result.Value, result.Precision, result.State = &parsed, "instant", "known"
			return result
		}
	}
	if parts := academicMonthDay.FindStringSubmatch(text); parts != nil {
		// A leap year validates February 29 without asserting any source year.
		if _, ok := academicCalendarTime(2000, academicNumber(parts[1]), academicNumber(parts[2]), academicNumber(parts[3]), academicNumber(parts[4]), 0, 0, academicLocation); ok {
			result.Precision = "month_day_minute"
		}
	}
	return result
}

// academicExamRange splits explicit range syntax without splitting date hyphens
// or RFC3339 offsets. An omitted end date is inherited only from an explicit,
// valid full start date. The end's Raw always remains its original endpoint.
func academicExamRange(raw, field string) (model.AcademicTime, model.AcademicTime) {
	left, right, ok := academicSplitRange(raw)
	if !ok {
		return parseAcademicTime(raw, field), parseAcademicTime("", field)
	}
	start, end := parseAcademicTime(left, field), parseAcademicTime(right, field)
	if clock := academicClock.FindStringSubmatch(strings.TrimSpace(right)); clock != nil {
		if date, _, explicit := academicExplicitTime(strings.TrimSpace(left)); explicit {
			if parsed, valid := academicCalendarTime(date.Year(), int(date.Month()), date.Day(), academicNumber(clock[1]), academicNumber(clock[2]), academicNumber(clock[3]), academicFraction(clock[4]), date.Location()); valid {
				parsed = parsed.UTC()
				end.Value, end.State, end.Precision = &parsed, "known", "local_minute"
				if clock[3] != "" {
					end.Precision = "instant"
				}
			}
		}
	}
	if start.Value != nil && end.Value != nil && end.Value.Before(*start.Value) {
		start.Value, end.Value = nil, nil
		start.State, end.State = "partial", "partial"
	}
	return start, end
}

func academicExplicitTime(text string) (time.Time, string, bool) {
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return parsed, "instant", true
	}
	parts := academicFullDate.FindStringSubmatch(text)
	if parts == nil {
		return time.Time{}, "", false
	}
	parsed, ok := academicCalendarTime(academicNumber(parts[1]), academicNumber(parts[2]), academicNumber(parts[3]), academicNumber(parts[4]), academicNumber(parts[5]), academicNumber(parts[6]), academicFraction(parts[7]), academicLocation)
	precision := "day"
	if parts[4] != "" {
		precision = "local_minute"
	}
	if parts[6] != "" {
		precision = "instant"
	}
	return parsed, precision, ok
}

func academicCalendarTime(year, month, day, hour, minute, second, nanosecond int, location *time.Location) (time.Time, bool) {
	if year < 1 || month < 1 || month > 12 || day < 1 || day > 31 || hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 59 {
		return time.Time{}, false
	}
	parsed := time.Date(year, time.Month(month), day, hour, minute, second, nanosecond, location)
	return parsed, parsed.Year() == year && int(parsed.Month()) == month && parsed.Day() == day && parsed.Hour() == hour && parsed.Minute() == minute
}

func academicNumber(text string) int {
	value, _ := strconv.Atoi(text)
	return value
}

func academicFraction(text string) int {
	if text == "" {
		return 0
	}
	return academicNumber(text + strings.Repeat("0", 9-len(text)))
}

func academicSplitRange(raw string) (string, string, bool) {
	separators := academicRangeSeparator.FindAllStringIndex(raw, -1)
	if len(separators) == 1 {
		separator := separators[0]
		return strings.TrimSpace(raw[:separator[0]]), strings.TrimSpace(raw[separator[1]:]), true
	}
	if len(separators) > 1 {
		return "", "", false
	}
	// A complete timestamp, including a negative RFC3339 offset, is not a range.
	if _, _, ok := academicExplicitTime(strings.TrimSpace(raw)); ok {
		return "", "", false
	}
	for index, character := range raw {
		if character != '-' {
			continue
		}
		left, right := strings.TrimSpace(raw[:index]), strings.TrimSpace(raw[index+1:])
		start, end := parseAcademicTime(left, ""), parseAcademicTime(right, "")
		// Bare hyphens require recognizable timestamp endpoints. Date-only
		// strings cannot accidentally turn their internal hyphens into ranges.
		if (start.State == "known" && start.Precision != "day" || start.Precision == "month_day_minute") && (end.State == "known" || end.Precision == "month_day_minute" || academicClock.MatchString(right)) {
			return left, right, true
		}
	}
	return "", "", false
}
