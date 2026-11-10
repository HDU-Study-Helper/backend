package platform

import (
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
	"context"
	"errors"
	"strings"
	"testing"
)

type verificationRepo struct{ testRepo }

func (r *verificationRepo) Targets(context.Context, string, int) ([]model.PlatformConnection, error) {
	return []model.PlatformConnection{r.state.Connection}, nil
}

type verificationAdapter struct {
	contract.Adapter
	err   error
	calls int
}

func (a *verificationAdapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "chaoxing", Resources: []contract.Resource{contract.Course, contract.Assignment, contract.Progress, contract.Discussion}}
}
func (a *verificationAdapter) Sync(context.Context, contract.Scope, contract.Session, contract.Resource, string) (contract.Page, error) {
	a.calls++
	return contract.Page{}, a.err
}
func (a *verificationAdapter) DiscussionCourses(context.Context, contract.Scope, contract.Session) ([]contract.CoursePartition, error) {
	return []contract.CoursePartition{{CourseID: "c1", ClassID: "k1"}, {CourseID: "c2", ClassID: "k2"}}, nil
}
func (a *verificationAdapter) SyncDiscussionCourse(context.Context, contract.Scope, contract.Session, string, string, string) (contract.Page, error) {
	a.calls++
	return contract.Page{}, a.err
}
func TestVerificationStopsConnectionBurst(t *testing.T) {
	for _, failure := range []error{contract.ErrVerificationRequired, contract.ErrRateLimited} {
		t.Run(failure.Error(), func(t *testing.T) {
			vault, _ := credential.New([]byte(strings.Repeat("k", 32)))
			sealed, _ := vault.Seal("owner", "connection", []byte("session"))
			repo := &verificationRepo{testRepo: testRepo{state: model.PlatformState{Connection: model.PlatformConnection{ID: "connection", UserID: "owner", Source: "chaoxing", State: "active", Enabled: true}, Session: sealed}}}
			a := &verificationAdapter{err: failure}
			s, e := New(repo, vault, []contract.Adapter{a})
			if e != nil {
				t.Fatal(e)
			}
			ctx, report := model.WithRefreshReport(context.Background())
			model.InitRefreshSource(ctx, "chaoxing", true)
			if !s.Poll(ctx) || a.calls != 1 {
				t.Fatal("continued scanning resources after upstream block", a.calls)
			}
			results := report.Snapshot()
			if len(results) != 1 || results[0].Attempts != 1 || results[0].Failed != 1 || results[0].LastError != failure.Error() {
				t.Fatal(results)
			}
			a.calls = 0
			e = s.Sync(context.Background(), model.DWSScope{UserID: "owner", ConnectionID: "connection"}, contract.Discussion)
			if !errors.Is(e, failure) || a.calls != 1 {
				t.Fatal("continued scanning courses after upstream block", a.calls, e)
			}
			if repo.failed != failure.Error() {
				t.Fatal("error code lost", repo.failed)
			}
		})
	}
}
