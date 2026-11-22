package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
)

type passwordAdapter struct {
	contract.Adapter
	logins, reads int
	year, term    int
	fail          error
	hasMore       bool
}

func (*passwordAdapter) Describe() contract.Descriptor {
	return contract.Descriptor{Source: "jwxt", Name: "教务", PasswordLogin: true, ManualSyncOnly: true, Resources: []contract.Resource{contract.Course, contract.Schedule}}
}
func (a *passwordAdapter) Login(_ context.Context, s contract.Scope, username, password string) (contract.AuthResult, error) {
	a.logins++
	if a.fail != nil {
		return contract.AuthResult{}, a.fail
	}
	if username != "student" || password != "password" {
		return contract.AuthResult{}, contract.ErrAuthRequired
	}
	return contract.AuthResult{Status: "authorized", Session: []byte("PRIVATE_SESSION"), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (a *passwordAdapter) SyncTerm(_ context.Context, _ contract.Scope, session contract.Session, r contract.Resource, year, term int, cursor string) (contract.Page, error) {
	a.reads++
	a.year = year
	a.term = term
	if string(session) != "PRIVATE_SESSION" {
		return contract.Page{}, contract.ErrAuthRequired
	}
	if a.fail != nil {
		return contract.Page{}, a.fail
	}
	next := ""
	if a.hasMore {
		next = "next-page"
	}
	return contract.Page{Records: []contract.Record{{Kind: r, ExternalID: "record", ParentID: "semester", Title: "脱敏课程", Fields: map[string]any{"academic_year": year, "term": term}}}, HasMore: a.hasMore, NextCursor: next}, nil
}

type passwordRepo struct {
	testRepo
	loaded, committed        string
	saves, acquires, targets int
	cancel                   context.CancelFunc
}

func (r *passwordRepo) Create(_ context.Context, owner, source string) (model.PlatformConnection, error) {
	if owner != r.state.Connection.UserID || source != "jwxt" {
		return model.PlatformConnection{}, model.ErrNotFound
	}
	return r.state.Connection, nil
}
func (r *passwordRepo) Get(_ context.Context, s model.DWSScope) (model.PlatformConnection, error) {
	if s.UserID != r.state.Connection.UserID || s.ConnectionID != r.state.Connection.ID {
		return model.PlatformConnection{}, model.ErrNotFound
	}
	return r.state.Connection, nil
}
func (r *passwordRepo) SaveAuth(_ context.Context, _ model.PlatformLease, s []byte, state string, expires time.Time) error {
	r.saves++
	r.state.Session = s
	r.state.Connection.State = state
	r.state.AuthExpiresAt = expires
	return nil
}
func (r *passwordRepo) Load(ctx context.Context, l model.PlatformLease, key string) (model.PlatformState, error) {
	r.loaded = key
	return r.testRepo.Load(ctx, l, key)
}
func (r *passwordRepo) Commit(ctx context.Context, l model.PlatformLease, kind string, records []model.PlatformRecord, session, cursor []byte) error {
	r.committed = l.CheckpointKey
	return r.testRepo.Commit(ctx, l, kind, records, session, cursor)
}
func (r *passwordRepo) Acquire(ctx context.Context, s model.DWSScope) (model.PlatformLease, error) {
	r.acquires++
	return r.testRepo.Acquire(ctx, s)
}
func (r *passwordRepo) Targets(context.Context, string, int) ([]model.PlatformConnection, error) {
	r.targets++
	if r.targets > 1 {
		r.cancel()
		return nil, context.Canceled
	}
	items := make([]model.PlatformConnection, 100)
	for i := range items {
		items[i] = r.state.Connection
	}
	return items, nil
}
func passwordFixture(t *testing.T) (*Service, *passwordRepo, *passwordAdapter) {
	t.Helper()
	v, e := credential.New([]byte(strings.Repeat("k", 32)))
	if e != nil {
		t.Fatal(e)
	}
	r := &passwordRepo{testRepo: testRepo{state: model.PlatformState{Connection: model.PlatformConnection{ID: "connection", UserID: "owner", Source: "jwxt", State: "pending", Enabled: true}}}}
	a := &passwordAdapter{}
	s, e := New(r, v, []contract.Adapter{a})
	if e != nil {
		t.Fatal(e)
	}
	return s, r, a
}
func TestPasswordLoginEncryptionAndTermCheckpoint(t *testing.T) {
	s, r, a := passwordFixture(t)
	ctx := context.Background()
	scope := model.DWSScope{UserID: "owner", ConnectionID: "connection"}
	c, e := s.LoginPassword(ctx, "owner", "jwxt", "student", "password")
	if e != nil || c.State != "active" || r.saves != 1 {
		t.Fatal("login not saved", e)
	}
	if strings.Contains(string(r.state.Session), "PRIVATE_SESSION") || strings.Contains(string(r.state.Session), "password") {
		t.Fatal("credentials persisted as plaintext")
	}
	plain, e := s.vault.Open("owner", "connection", r.state.Session)
	if e != nil || string(plain) != "PRIVATE_SESSION" {
		t.Fatal("session cannot be restored", e)
	}
	if _, e = s.vault.Open("other", "connection", r.state.Session); e == nil {
		t.Fatal("ciphertext not owner-bound")
	}
	if e = s.SyncTerm(ctx, scope, contract.Course, 2026, 1); e != nil || r.loaded != "course:2026:1" || r.committed != r.loaded || len(r.commits) != 1 {
		t.Fatal("term checkpoint missing", e, r.loaded, r.committed)
	}
	if e = s.SyncTerm(ctx, scope, contract.Course, 2025, 2); e != nil || r.loaded != "course:2025:2" || a.year != 2025 || a.term != 2 {
		t.Fatal("term selection lost", e)
	}
	a.fail = contract.ErrParse
	previous := string(r.next)
	if e = s.SyncTerm(ctx, scope, contract.Course, 2025, 2); !errors.Is(e, contract.ErrParse) || string(r.next) != previous {
		t.Fatal("failed read advanced checkpoint")
	}
	if e = s.SyncTerm(ctx, model.DWSScope{UserID: "other", ConnectionID: "connection"}, contract.Course, 2026, 1); !errors.Is(e, model.ErrNotFound) {
		t.Fatal("cross-owner sync accepted", e)
	}
	if e = s.SyncTerm(ctx, scope, contract.Schedule, 2026, 0); !errors.Is(e, contract.ErrInvalid) {
		t.Fatal("invalid semester accepted")
	}
}
func TestFailedPasswordLoginDoesNotSaveCredentials(t *testing.T) {
	s, r, a := passwordFixture(t)
	a.fail = contract.ErrAuthRequired
	if _, e := s.LoginPassword(context.Background(), "owner", "jwxt", "student", "password"); !errors.Is(e, contract.ErrAuthRequired) || r.saves != 0 || len(r.state.Session) > 0 {
		t.Fatal("failed login stored credentials", e)
	}
	if _, e := s.LoginPassword(context.Background(), "owner", "chaoxing", "student", "password"); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("password login accepted on unsupported source")
	}
}
func TestManualOnlyAdapterSkippedBySharedRefreshScan(t *testing.T) {
	for _, state := range []string{"active", "pending"} {
		t.Run(state, func(t *testing.T) {
			s, r, a := passwordFixture(t)
			r.state.Connection.State = state
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r.cancel = cancel
			if failed := s.Poll(ctx); !failed || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("scan did not report repository cancellation")
			}
			if r.targets != 2 || r.acquires != 0 || a.logins != 0 || a.reads != 0 {
				t.Fatal("manual-only connection was accessed by worker")
			}
		})
	}
}
func TestPasswordAndAcademicHTTP(t *testing.T) {
	s, r, _ := passwordFixture(t)
	request := func(path, body, owner string) *httptest.ResponseRecorder {
		rq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		if owner != "" {
			rq = rq.WithContext(httpkit.WithUser(rq.Context(), model.User{ID: owner}))
		}
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, rq)
		return w
	}
	if w := request("/jwxt/login", `{"username":"student","password":"password"}`, ""); w.Code != 401 {
		t.Fatal("unauthenticated login allowed", w.Code)
	}
	w := request("/jwxt/login", `{"username":"student","password":"password"}`, "owner")
	if w.Code != 201 || strings.Contains(w.Body.String(), "PRIVATE_SESSION") || strings.Contains(w.Body.String(), "password") {
		t.Fatal("login response wrong or leaked credentials", w.Code, w.Body.String())
	}
	if w = request("/connections/connection/sync", `{"resource":"course","academic_year":2026,"term":1}`, "owner"); w.Code != 200 || r.committed != "course:2026:1" {
		t.Fatal("manual schedule route failed", w.Code, w.Body.String())
	}
	var receipt SyncPageReceipt
	if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Status != "page_committed" || receipt.PageCount != 1 || receipt.HasMore || receipt.AcademicYear != 2026 || receipt.Term != 1 {
		t.Fatal("missing committed page metadata", w.Body.String())
	}
	var record contract.Record
	if json.Unmarshal(r.commits[0].Content, &record) != nil || record.Kind != contract.Course {
		t.Fatal("wrong archived record")
	}
	if w = request("/connections/connection/sync", `{"resource":"course","academic_year":2026,"term":2}`, "other"); w.Code != 404 {
		t.Fatal("foreign sync accepted", w.Code)
	}
	for _, body := range []string{`{"resource":"schedule","academic_year":2026}`, `{"resource":"schedule","academic_year":2026,"term":3}`, `{"resource":"schedule","academic_year":2026,"term":1,"course_id":"x"}`} {
		if w = request("/connections/connection/sync", body, "owner"); w.Code != 400 {
			t.Fatal("invalid academic sync accepted", w.Code)
		}
	}
}
