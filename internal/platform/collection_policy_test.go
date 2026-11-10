package platform

import (
	"academic/backend/internal/contract"
	"academic/backend/internal/model"
	"context"
	"errors"
	"testing"
)

type policyRepo struct {
	*collectRepo
	refs      []string
	policyErr error
	reads     int
}

func (r *policyRepo) CollectionPolicy(context.Context, string) (model.CollectionPolicy, error) {
	return model.CollectionPolicy{}, nil
}
func (r *policyRepo) SaveCollectionPolicy(context.Context, string, int, []string) (model.CollectionPolicy, error) {
	return model.CollectionPolicy{}, nil
}
func (r *policyRepo) ExcludedCourseRefs(_ context.Context, s model.DWSScope) ([]string, error) {
	r.reads++
	if s.UserID != "owner" || s.ConnectionID != "connection" {
		return nil, model.ErrNotFound
	}
	return r.refs, r.policyErr
}

type policyAdapter struct {
	*collectAdapter
	seen  []bool
	after func()
}

func (a *policyAdapter) Sync(ctx context.Context, s contract.Scope, _ contract.Session, r contract.Resource, cursor string) (contract.Page, error) {
	a.seen = append(a.seen, contract.CourseExcluded(ctx, s, "course-ref"))
	if a.after != nil {
		a.after()
	}
	return contract.Page{Records: []contract.Record{}, HasMore: cursor == "", NextCursor: map[bool]string{true: "next"}[cursor == ""]}, nil
}
func TestCollectionPolicyReloadsEachPageAndFailsClosed(t *testing.T) {
	a := &policyAdapter{collectAdapter: &collectAdapter{}}
	s, base := collectFixture(t, a)
	r := &policyRepo{collectRepo: base}
	s.repo = r
	a.after = func() { r.refs = []string{"course-ref"} }
	scope := model.DWSScope{UserID: "owner", ConnectionID: "connection"}
	if _, err := s.syncPageResult(context.Background(), scope, contract.Course, "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncPageResult(context.Background(), scope, contract.Course, "", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if len(a.seen) != 2 || a.seen[0] || !a.seen[1] || r.reads != 2 {
		t.Fatal("policy was not refreshed between pages", a.seen, r.reads)
	}
	r.policyErr = errors.New("policy unavailable")
	if _, err := s.syncPageResult(context.Background(), scope, contract.Course, "", "", 0, 0); err == nil || len(a.seen) != 2 {
		t.Fatal("policy failure allowed source read")
	}
}
