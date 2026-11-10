package platform

import (
	"context"
	"errors"
	"strings"
	"testing"

	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
	"academic/backend/internal/schedule"
)

type snapshotRepo struct {
	passwordRepo
	refreshes int
	bundle    model.TimetableRefresh
}

func (r *snapshotRepo) CommitTimetable(_ context.Context, _ model.PlatformLease, records []model.PlatformRecord, _ []byte, b model.TimetableRefresh) (model.TimetableResult, error) {
	r.refreshes++
	r.bundle = b
	return model.TimetableResult{Semester: b.Semester, Records: len(records), Generated: len(b.Schedules)}, nil
}

type snapshotAdapter struct {
	passwordAdapter
	badSecond    bool
	invalidWeeks bool
	calls        int
	repeatCursor bool
}

func (a *snapshotAdapter) SyncTerm(_ context.Context, _ contract.Scope, _ contract.Session, _ contract.Resource, year, term int, cursor string) (contract.Page, error) {
	a.calls++
	if cursor != "" && a.badSecond {
		return contract.Page{}, contract.ErrUpstream
	}
	id, title, more, next := "one", "数学", true, "next"
	if cursor != "" {
		id, title, more, next = "two", "物理", false, ""
	}
	if a.repeatCursor && cursor != "" {
		more, next = true, "next"
	}
	weeks := "8-9周"
	if a.invalidWeeks {
		weeks = "未知周次"
	}
	return contract.Page{Records: []contract.Record{{Kind: contract.Schedule, ExternalID: id, Title: title, ParentID: `["2026","1"]`, Fields: map[string]any{"academic_year": year, "term": term, "entry_type": "class_session", "course_id": id, "class_id": id, "weekday": 3, "weeks_raw": weeks, "periods_raw": "1-2"}}}, HasMore: more, NextCursor: next}, nil
}
func TestCompleteTimetableSnapshotBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		badSecond, invalidWeeks, repeat bool
		want                            error
	}{{"complete", false, false, false, nil}, {"later page failure", true, false, false, contract.ErrUpstream}, {"unknown weeks", false, true, false, contract.ErrParse}, {"repeated cursor", false, false, true, contract.ErrParse}} {
		t.Run(tc.name, func(t *testing.T) {
			v, e := credential.New([]byte(strings.Repeat("k", 32)))
			if e != nil {
				t.Fatal(e)
			}
			repo := &snapshotRepo{}
			repo.state.Connection = model.PlatformConnection{ID: "connection", UserID: "owner", Source: "jwxt", State: "active", Enabled: true}
			repo.state.Session, e = v.Seal("owner", "connection", []byte("session"))
			if e != nil {
				t.Fatal(e)
			}
			a := &snapshotAdapter{badSecond: tc.badSecond, invalidWeeks: tc.invalidWeeks, repeatCursor: tc.repeat}
			s, e := New(repo, v, []contract.Adapter{a})
			if e != nil {
				t.Fatal(e)
			}
			calendar := schedule.Calendar{WeekOneMonday: "2026-09-07", Periods: []schedule.PeriodTime{{Period: 1, Start: "08:00", End: "08:45"}, {Period: 2, Start: "08:50", End: "09:35"}}}
			result, e := s.SyncTimetable(context.Background(), model.DWSScope{UserID: "owner", ConnectionID: "connection"}, model.Semester{AcademicYear: 2026, Term: 1}, calendar)
			if !errors.Is(e, tc.want) {
				t.Fatal(e, tc.want)
			}
			if tc.want != nil {
				if repo.refreshes != 0 {
					t.Fatal("partial/invalid snapshot committed")
				}
			} else if repo.refreshes != 1 || result.Generated != 4 || result.Records != 2 || len(repo.bundle.Schedules) != 4 {
				t.Fatal("not a single complete commit", repo.refreshes, result)
			}
		})
	}
}
