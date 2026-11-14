package platform

import (
	"academic/backend/internal/contract"
	"academic/backend/internal/credential"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type refreshRepo struct {
	*testRepo
	saves   int
	saveErr error
}

func (r *refreshRepo) SaveAuth(_ context.Context, _ model.PlatformLease, session []byte, state string, expiry time.Time) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saves++
	r.state.Session = session
	r.state.Connection.State = state
	r.state.AuthExpiresAt = expiry
	return nil
}

type refreshAdapter struct {
	*testAdapter
	calls, refreshes    int
	refreshErr, readErr error
	inputs              []string
	cursors             []string
	keepFail            bool
}

func (a *refreshAdapter) RefreshSession(_ context.Context, _ contract.Scope, session contract.Session) (contract.Session, error) {
	a.refreshes++
	if string(session) != "old-cookie" {
		return nil, contract.ErrInvalid
	}
	if a.refreshErr != nil {
		return nil, a.refreshErr
	}
	return []byte("new-cookie"), nil
}
func (a *refreshAdapter) Sync(_ context.Context, _ contract.Scope, session contract.Session, _ contract.Resource, cursor string) (contract.Page, error) {
	a.calls++
	a.inputs = append(a.inputs, string(session))
	a.cursors = append(a.cursors, cursor)
	if a.readErr != nil {
		return contract.Page{}, a.readErr
	}
	if a.calls == 1 || a.keepFail {
		return contract.Page{}, contract.ErrAuthRequired
	}
	return contract.Page{Records: []contract.Record{}, NextCursor: "advanced"}, nil
}
func refreshFixture(t *testing.T) (*Service, *refreshRepo, *refreshAdapter, *credential.Vault, model.DWSScope) {
	t.Helper()
	v, e := credential.New([]byte(strings.Repeat("k", 32)))
	if e != nil {
		t.Fatal(e)
	}
	session, e := v.Seal("owner", "connection", []byte("old-cookie"))
	if e != nil {
		t.Fatal(e)
	}
	cursor, e := v.Seal("owner", "connection", []byte("original-cursor"))
	if e != nil {
		t.Fatal(e)
	}
	r := &refreshRepo{testRepo: &testRepo{state: model.PlatformState{Connection: model.PlatformConnection{UserID: "owner", ID: "connection", Source: "chaoxing", State: "active", Enabled: true}, Session: session, Cursor: cursor, AuthExpiresAt: time.Unix(123, 0)}}}
	a := &refreshAdapter{testAdapter: &testAdapter{}}
	s, e := New(r, v, []contract.Adapter{a})
	if e != nil {
		t.Fatal(e)
	}
	return s, r, a, v, model.DWSScope{UserID: "owner", ConnectionID: "connection"}
}
func TestSyncAuthRecoveryPersistsEncryptedSessionAndOriginalCursor(t *testing.T) {
	s, r, a, v, scope := refreshFixture(t)
	if e := s.Sync(context.Background(), scope, contract.Course); e != nil {
		t.Fatal(e)
	}
	if a.calls != 2 || a.refreshes != 1 || r.saves != 1 || a.inputs[0] != "old-cookie" || a.inputs[1] != "new-cookie" || a.cursors[0] != "original-cursor" || a.cursors[1] != "original-cursor" {
		t.Fatal(a, r.saves)
	}
	plain, e := v.Open(scope.UserID, scope.ConnectionID, r.state.Session)
	if e != nil || string(plain) != "new-cookie" {
		t.Fatal("refreshed session absent or not encrypted", e)
	}
	if string(r.state.Session) == "new-cookie" {
		t.Fatal("plaintext persisted")
	}
}
func TestRefreshDoesNotChangeCheckpointAndCanRecoverExpiredState(t *testing.T) {
	s, r, a, v, scope := refreshFixture(t)
	r.state.Connection.State = "expired"
	before := append([]byte(nil), r.state.Cursor...)
	expiry := r.state.AuthExpiresAt
	c, e := s.RefreshSession(context.Background(), scope)
	if e != nil || c.State != "active" || a.calls != 0 || a.refreshes != 1 || string(before) != string(r.state.Cursor) || !expiry.Equal(r.state.AuthExpiresAt) {
		t.Fatal(c, e, a)
	}
	if _, e = v.Open(scope.UserID, scope.ConnectionID, r.state.Session); e != nil {
		t.Fatal(e)
	}
}
func TestSessionRefreshFailsClosedAndNeverLoops(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		setup                   func(*refreshRepo, *refreshAdapter)
		want                    error
		calls, refreshes, saves int
	}{{"parse", func(r *refreshRepo, a *refreshAdapter) { a.readErr = contract.ErrParse }, contract.ErrParse, 1, 0, 0}, {"rate limit", func(r *refreshRepo, a *refreshAdapter) { a.readErr = contract.ErrRateLimited }, contract.ErrRateLimited, 1, 0, 0}, {"upstream", func(r *refreshRepo, a *refreshAdapter) { a.readErr = contract.ErrUpstream }, contract.ErrUpstream, 1, 0, 0}, {"refresh auth failure", func(r *refreshRepo, a *refreshAdapter) { a.refreshErr = contract.ErrAuthRequired }, contract.ErrAuthRequired, 1, 1, 0}, {"refresh network failure", func(r *refreshRepo, a *refreshAdapter) { a.refreshErr = contract.ErrUpstream }, contract.ErrUpstream, 1, 1, 0}, {"retry auth failure", func(r *refreshRepo, a *refreshAdapter) { a.keepFail = true }, contract.ErrAuthRequired, 2, 1, 1}, {"revoked lease", func(r *refreshRepo, a *refreshAdapter) { r.saveErr = model.ErrLeaseLost }, model.ErrLeaseLost, 1, 1, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, a, _, scope := refreshFixture(t)
			tc.setup(r, a)
			e := s.Sync(context.Background(), scope, contract.Course)
			if !errors.Is(e, tc.want) || a.calls != tc.calls || a.refreshes != tc.refreshes || r.saves != tc.saves {
				t.Fatal(e, a.calls, a.refreshes, r.saves)
			}
		})
	}
}
func TestRefreshSessionHTTPReturnsNoCookies(t *testing.T) {
	s, _, a, _, _ := refreshFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/connections/connection/refresh-session", nil)
	req = req.WithContext(httpkit.WithUser(req.Context(), model.User{ID: "owner"}))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != 200 || a.refreshes != 1 || !strings.Contains(w.Body.String(), `"session_refreshed":true`) || strings.Contains(w.Body.String(), "cookie") {
		t.Fatal(w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/connections/connection/refresh-session", nil)
	req = req.WithContext(httpkit.WithUser(req.Context(), model.User{ID: "other"}))
	w = httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("foreign refresh accepted", w.Code)
	}
}

func TestExplicitRefreshRejectsDisconnectedAndCrossOwner(t *testing.T) {
	s, r, a, _, scope := refreshFixture(t)
	r.state.Connection.Enabled = false
	if _, e := s.RefreshSession(context.Background(), scope); !errors.Is(e, contract.ErrAuthRequired) || a.refreshes != 0 {
		t.Fatal(e, a)
	}
	if _, e := s.RefreshSession(context.Background(), model.DWSScope{UserID: "other", ConnectionID: scope.ConnectionID}); !errors.Is(e, model.ErrNotFound) {
		t.Fatal(e)
	}
}
