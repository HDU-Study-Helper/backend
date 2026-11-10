package platform

import (
	"context"
	"errors"
	"strings"
	"testing"

	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
)

type collectRepo struct{ *testRepo }

func (r collectRepo) Targets(context.Context, string, int) ([]model.PlatformConnection, error) {
	return []model.PlatformConnection{r.state.Connection}, nil
}

type collectAdapter struct {
	contract.Adapter
	calls   int
	failure error
	endless bool
}

func (a *collectAdapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "chaoxing", Resources: []contract.Resource{contract.Course, contract.Assignment}}
}
func (a *collectAdapter) Sync(_ context.Context, _ contract.Scope, _ contract.Session, resource contract.Resource, cursor string) (contract.Page, error) {
	a.calls++
	if a.failure != nil {
		return contract.Page{}, a.failure
	}
	more := cursor == "" || a.endless
	next := ""
	if more {
		next = cursor + "n"
	}
	return contract.Page{Records: []contract.Record{{Kind: resource, ExternalID: "record"}}, NextCursor: next, HasMore: more}, nil
}
func collectFixture(t *testing.T, a contract.Adapter) (*Service, *collectRepo) {
	t.Helper()
	v, e := credential.New([]byte(strings.Repeat("k", 32)))
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := v.Seal("owner", "connection", []byte("session"))
	if e != nil {
		t.Fatal(e)
	}
	r := &collectRepo{&testRepo{state: model.PlatformState{Connection: model.PlatformConnection{ID: "connection", UserID: "owner", Source: "chaoxing", State: "active", Enabled: true}, Session: sealed}}}
	s, e := New(r, v, []contract.Adapter{a})
	if e != nil {
		t.Fatal(e)
	}
	return s, r
}
func TestCollectOnceDrainsPagesWithoutRetry(t *testing.T) {
	a := &collectAdapter{}
	s, _ := collectFixture(t, a)
	ctx, report := model.WithRefreshReport(context.Background())
	if e := s.collectOnce(ctx, 10, 5); e != nil {
		t.Fatal(e)
	}
	if a.calls != 4 {
		t.Fatalf("expected both resources complete in two pages, got %d", a.calls)
	}
	results := report.Snapshot()
	if len(results) != 1 || results[0].Succeeded != 4 {
		t.Fatal(results)
	}
}
func TestCollectOnceStopsConnectionAtVerification(t *testing.T) {
	a := &collectAdapter{failure: contract.ErrVerificationRequired}
	s, _ := collectFixture(t, a)
	if e := s.collectOnce(context.Background(), 10, 5); !errors.Is(e, contract.ErrVerificationRequired) {
		t.Fatal(e)
	}
	if a.calls != 1 {
		t.Fatal("retried failed page or fetched next resource", a.calls)
	}
}
func TestCollectOnceBudgetAndDisabled(t *testing.T) {
	a := &collectAdapter{endless: true}
	s, r := collectFixture(t, a)
	if e := s.collectOnce(context.Background(), 3, 2); e == nil {
		t.Fatal("unbounded pagination accepted")
	}
	if a.calls > 3 {
		t.Fatal(a.calls)
	}
	a.calls = 0
	r.state.Connection.Enabled = false
	if e := s.collectOnce(context.Background(), 3, 2); e != nil || a.calls != 0 {
		t.Fatal(e, a.calls)
	}
}
