package dbbridge

import (
	"testing"
	"time"

	"academic/backend/internal/model"
)

func TestParseAcademicTime(t *testing.T) {
	tests := []struct {
		name, raw, state, precision, value string
	}{
		{"empty", "", "missing", "unknown", ""},
		{"blank evidence", " \t\n", "missing", "unknown", ""},
		{"unknown", "待定", "partial", "unknown", ""},
		{"invalid date", "2025-02-29", "partial", "unknown", ""},
		{"invalid month", "2025-13-01 09:30", "partial", "unknown", ""},
		{"invalid clock", "2025-06-08 24:00", "partial", "unknown", ""},
		{"invalid minute", "2025-06-08 09:60", "partial", "unknown", ""},
		{"invalid second", "2025-06-08 09:30:60", "partial", "unknown", ""},
		{"year zero", "0000-06-08", "partial", "unknown", ""},
		{"yearless", "06-08 09:30", "partial", "month_day_minute", ""},
		{"yearless single digits", "6-8 9:30", "partial", "month_day_minute", ""},
		{"yearless Chinese", "6月8日 09:30", "partial", "month_day_minute", ""},
		{"yearless slash", "6/8 09:30", "partial", "month_day_minute", ""},
		{"yearless leap day", "02-29 09:30", "partial", "month_day_minute", ""},
		{"yearless invalid date", "02-30 09:30", "partial", "unknown", ""},
		{"yearless invalid clock", "06-08 25:30", "partial", "unknown", ""},
		{"clock without date", "09:30", "partial", "unknown", ""},
		{"milliseconds", "1749346200123", "known", "instant", "2025-06-08T01:30:00.123Z"},
		{"seconds", "1749346200", "known", "instant", "2025-06-08T01:30:00Z"},
		{"counter", "12345", "partial", "unknown", ""},
		{"compact date is not epoch", "20250608", "partial", "unknown", ""},
		{"zero sentinel", "0", "partial", "unknown", ""},
		{"negative sentinel", "-1", "partial", "unknown", ""},
		{"overflow", "999999999999999999999999", "partial", "unknown", ""},
		{"RFC3339", "2025-06-08T09:30:00+08:00", "known", "instant", "2025-06-08T01:30:00Z"},
		{"RFC3339 fractions", "2025-06-08T01:30:00.123456789Z", "known", "instant", "2025-06-08T01:30:00.123456789Z"},
		{"RFC3339 negative offset", "2025-06-08T09:30:00-07:00", "known", "instant", "2025-06-08T16:30:00Z"},
		{"local minute", "2025-06-08 09:30", "known", "local_minute", "2025-06-08T01:30:00Z"},
		{"trim while preserving evidence", " 2025-06-08 09:30 \n", "known", "local_minute", "2025-06-08T01:30:00Z"},
		{"local seconds", "2025-06-08 09:30:15", "known", "instant", "2025-06-08T01:30:15Z"},
		{"local fractions", "2025-06-08 09:30:15.123456789", "known", "instant", "2025-06-08T01:30:15.123456789Z"},
		{"local T", "2025-06-08T09:30", "known", "local_minute", "2025-06-08T01:30:00Z"},
		{"local slash", "2025/6/8 9:30", "known", "local_minute", "2025-06-08T01:30:00Z"},
		{"local Chinese", "2025年6月8日 09:30", "known", "local_minute", "2025-06-08T01:30:00Z"},
		{"date", "2025-06-08", "known", "day", "2025-06-08"},
		{"leap date", "2024-02-29", "known", "day", "2024-02-29"},
		{"Chinese date", "2025年6月8日", "known", "day", "2025-06-08"},
		{"date trailing text", "2025-06-08 待定", "partial", "unknown", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseAcademicTime(test.raw, "source.deadline")
			assertAcademicTime(t, got, test.raw, "source.deadline", test.state, test.precision, test.value)
		})
	}
}

func TestAcademicExamRange(t *testing.T) {
	tests := []struct {
		name, raw, left, right, start, end, startPrecision, endPrecision string
	}{
		{"tilde", "2025-06-08 09:30~11:00", "2025-06-08 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"wide tilde", "2025-06-08 09:30 ～ 11:00", "2025-06-08 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"Chinese to", "2025年6月8日 09:30至11:00", "2025年6月8日 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"Chinese dash", "2025-06-08 09:30—11:00", "2025-06-08 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"en dash", "2025-06-08 09:30–11:00", "2025-06-08 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"wide dash", "2025-06-08 09:30－11:00", "2025-06-08 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"spaced hyphen", "2025-06-08 09:30 - 11:00", "2025-06-08 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"bare hyphen", "2025-06-08 09:30-11:00", "2025-06-08 09:30", "11:00", "2025-06-08T01:30:00Z", "2025-06-08T03:00:00Z", "local_minute", "local_minute"},
		{"full endpoints", "2025-06-08 09:30-2025-06-09 11:00", "2025-06-08 09:30", "2025-06-09 11:00", "2025-06-08T01:30:00Z", "2025-06-09T03:00:00Z", "local_minute", "local_minute"},
		{"date endpoints", "2025-06-08 至 2025-06-09", "2025-06-08", "2025-06-09", "2025-06-08", "2025-06-09", "day", "day"},
		{"date and clock", "2025-06-08 至 11:00", "2025-06-08", "11:00", "2025-06-08", "2025-06-08T03:00:00Z", "day", "local_minute"},
		{"RFC offset range", "2025-06-08T09:30:00-07:00-2025-06-08T11:00:00-07:00", "2025-06-08T09:30:00-07:00", "2025-06-08T11:00:00-07:00", "2025-06-08T16:30:00Z", "2025-06-08T18:00:00Z", "instant", "instant"},
		{"RFC date inheritance preserves offset", "2025-06-08T23:30:00-07:00~23:45:15.25", "2025-06-08T23:30:00-07:00", "23:45:15.25", "2025-06-09T06:30:00Z", "2025-06-09T06:45:15.25Z", "instant", "instant"},
		{"no guessed rollover", "2025-12-31 23:00~01:00", "2025-12-31 23:00", "01:00", "", "", "local_minute", "local_minute"},
		{"reversed explicit dates", "2025-06-09 09:30~2025-06-08 11:00", "2025-06-09 09:30", "2025-06-08 11:00", "", "", "local_minute", "local_minute"},
		{"reversed epochs", "1749351600000~1749346200000", "1749351600000", "1749346200000", "", "", "instant", "instant"},
		{"reversed offsets", "2025-06-08T09:30:00-07:00~2025-06-08T11:00:00+08:00", "2025-06-08T09:30:00-07:00", "2025-06-08T11:00:00+08:00", "", "", "instant", "instant"},
		{"equal instants", "2025-06-08 09:30~09:30", "2025-06-08 09:30", "09:30", "2025-06-08T01:30:00Z", "2025-06-08T01:30:00Z", "local_minute", "local_minute"},
		{"yearless endpoints", "06-08 09:30-06-08 11:00", "06-08 09:30", "06-08 11:00", "", "", "month_day_minute", "month_day_minute"},
		{"no yearless inference", "06-08 09:30~11:00", "06-08 09:30", "11:00", "", "", "month_day_minute", "unknown"},
		{"no epoch date inference", "1749346200000~11:00", "1749346200000", "11:00", "2025-06-08T01:30:00Z", "", "instant", "unknown"},
		{"invalid date cannot supply end date", "2025-02-29 09:30~11:00", "2025-02-29 09:30", "11:00", "", "", "unknown", "unknown"},
		{"invalid end clock", "2025-06-08 09:30~25:00", "2025-06-08 09:30", "25:00", "2025-06-08T01:30:00Z", "", "local_minute", "unknown"},
		{"unknown explicit range", "待定 至 待通知", "待定", "待通知", "", "", "unknown", "unknown"},
		{"single RFC offset", "2025-06-08T09:30:00-07:00", "2025-06-08T09:30:00-07:00", "", "2025-06-08T16:30:00Z", "", "instant", "unknown"},
		{"single date", "2025-06-08", "2025-06-08", "", "2025-06-08", "", "day", "unknown"},
		{"unknown whole range", "待安排", "待安排", "", "", "", "unknown", "unknown"},
		{"ambiguous multiple ranges", "2025-06-08 09:30~10:00~11:00", "2025-06-08 09:30~10:00~11:00", "", "", "", "unknown", "unknown"},
		{"empty start", "~11:00", "", "11:00", "", "", "unknown", "unknown"},
		{"empty end", "2025-06-08 09:30~", "2025-06-08 09:30", "", "2025-06-08T01:30:00Z", "", "local_minute", "unknown"},
		{"empty", "", "", "", "", "", "unknown", "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end := academicExamRange(test.raw, "exam_time")
			state := func(raw, value string) string {
				if raw == "" {
					return "missing"
				}
				if value == "" {
					return "partial"
				}
				return "known"
			}
			assertAcademicTime(t, start, test.left, "exam_time", state(test.left, test.start), test.startPrecision, test.start)
			assertAcademicTime(t, end, test.right, "exam_time", state(test.right, test.end), test.endPrecision, test.end)
		})
	}
}

func assertAcademicTime(t *testing.T, got model.AcademicTime, raw, field, state, precision, value string) {
	t.Helper()
	if got.Raw != raw || got.SourceField != field || got.State != state || got.Precision != precision {
		t.Fatalf("got %+v; want raw=%q field=%q state=%q precision=%q", got, raw, field, state, precision)
	}
	if precision == "day" {
		if got.Date != value || got.Value != nil {
			t.Fatalf("date evidence = %+v; want Date=%q and nil Value", got, value)
		}
		return
	}
	if got.Date != "" {
		t.Fatalf("unexpected date-only evidence %q", got.Date)
	}
	if value == "" {
		if got.Value != nil {
			t.Fatalf("unexpected inferred value %s", got.Value.Format(time.RFC3339Nano))
		}
		return
	}
	want, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value == nil || !got.Value.Equal(want) {
		t.Fatalf("value = %v; want %s", got.Value, value)
	}
	if got.Value.Location() != time.UTC {
		t.Fatalf("value must be normalized to UTC, got %v", got.Value.Location())
	}
}
