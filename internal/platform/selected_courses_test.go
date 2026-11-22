package platform

import (
	"academic/backend/internal/contract"
	"academic/backend/internal/model"
	"context"
	"testing"
)

func TestSelectedCoursePageReceiptRequiresCommit(t *testing.T) {
	s, r, a := passwordFixture(t)
	ctx := context.Background()
	if _, e := s.LoginPassword(ctx, "owner", "jwxt", "student", "password"); e != nil {
		t.Fatal(e)
	}
	scope := model.DWSScope{UserID: "owner", ConnectionID: "connection"}
	a.hasMore = true
	page, e := s.SyncTermPage(ctx, scope, contract.Course, 2026, 1)
	if e != nil || page.PageCount != 1 || !page.HasMore || page.Status != "page_committed" {
		t.Fatal(page, e)
	}
	if a.reads != 1 {
		t.Fatal("manual action followed upstream pages automatically", a.reads)
	}
	r.reject = true
	failed, e := s.SyncTermPage(ctx, scope, contract.Course, 2026, 1)
	if e == nil || failed != (SyncPageReceipt{}) {
		t.Fatal("failed commit returned success/continuation", failed, e)
	}
}
func TestSelectedCoursePageRejectsInvalidSemesterBeforeRead(t *testing.T) {
	s, _, a := passwordFixture(t)
	for _, year := range []int{0, 1999, 2101} {
		p, e := s.SyncTermPage(context.Background(), model.DWSScope{UserID: "owner", ConnectionID: "connection"}, contract.Course, year, 1)
		if e == nil || p != (SyncPageReceipt{}) {
			t.Fatal(p, e)
		}
	}
	if a.reads != 0 {
		t.Fatal(a.reads)
	}
}
