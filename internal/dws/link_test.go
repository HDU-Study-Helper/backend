package dws

import (
	"academic/backend/internal/httpkit"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"academic/backend/internal/model"
)

func TestOfficialDevicePresentation(t *testing.T) {
	var got []deviceChallenge
	cancelled := false
	p := &deviceParser{cancel: func() { cancelled = true }, emit: func(d deviceChallenge) error { got = append(got, d); return nil }}
	fixture := "✓ Step 1: 请求设备授权码...\n\n  授权码: TEST-CODE\n  授权码将在 300 秒后过期。\n\n  授权链接（已填入授权码）：\nhttps://login.dingtalk.com/device?code=TEST-CODE\n\n  手动输入授权码的链接：\nhttps://login.dingtalk.com/device\n\n"
	for _, b := range []byte(fixture) {
		_, _ = p.Write([]byte{b})
	}
	if cancelled || len(got) != 2 || got[1].uri != "https://login.dingtalk.com/device?code=TEST-CODE" || got[1].lifetime != 300*time.Second {
		t.Fatal("official fixture not recognized")
	}
	_, _ = p.Write([]byte("  授权码: NEXT\n"))
	if got[len(got)-1].uri != "" || p.uri != "" {
		t.Fatal("retry retained old URL")
	}
}
func TestDeviceRejectsUntrustedURLs(t *testing.T) {
	for _, raw := range []string{"http://login.dingtalk.com/a", "https://dingtalk.com.evil.test/a", "https://evil.test/a", "https://user@dingtalk.com/a", "https://dingtalk.com:443/a"} {
		if officialDeviceURL(raw) {
			t.Fatal("accepted untrusted URL")
		}
		p := &deviceParser{cancel: func() {}, emit: func(deviceChallenge) error { return nil }}
		_, _ = p.Write([]byte("  授权码: TEST\n  授权码将在 10 秒后过期。\n  授权链接（已填入授权码）：\n" + raw + "\n"))
		if !p.failed {
			t.Fatal("parser accepted untrusted URL")
		}
	}
}

type linkRepo struct {
	*fakeRepo
	mu        sync.Mutex
	disabled  bool
	saves     int
	state     string
	failSave  bool
	empty     bool
	creates   int
	identity  model.DWSIdentity
	workspace []byte
}

func (r *linkRepo) GetConnection(ctx context.Context, s model.DWSScope) (model.DWSConnection, error) {
	c, e := r.fakeRepo.GetConnection(ctx, s)
	r.mu.Lock()
	defer r.mu.Unlock()
	c.State = "pending"
	if r.empty {
		c.Profile, c.CorpID, c.UpstreamUserID = "", "", ""
	}
	c.Enabled = !r.disabled
	return c, e
}
func (r *linkRepo) CreatePendingConnection(ctx context.Context, owner string) (model.DWSConnection, error) {
	r.mu.Lock()
	r.creates++
	r.mu.Unlock()
	return r.GetConnection(ctx, model.DWSScope{UserID: owner, ConnectionID: r.scope.ConnectionID})
}
func (r *linkRepo) CompleteLink(ctx context.Context, l model.Lease, identity model.DWSIdentity, b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled || r.failSave || l.Scope != r.scope || l.Token != r.lease.Token {
		return model.ErrLeaseLost
	}
	r.saves++
	r.state = "active"
	r.identity = identity
	r.workspace = append([]byte(nil), b...)
	return nil
}
func (r *linkRepo) SaveWorkspace(ctx context.Context, l model.Lease, b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled || r.failSave {
		return model.ErrLeaseLost
	}
	r.saves++
	return nil
}
func (r *linkRepo) UpdateConnection(ctx context.Context, s model.DWSScope, u model.DWSConnectionUpdate) (model.DWSConnection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return model.DWSConnection{}, model.ErrNotFound
	}
	r.state = *u.State
	return model.DWSConnection{}, nil
}
func (r *linkRepo) Disconnect(context.Context, model.DWSScope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disabled = true
	return nil
}

type fakeAuth struct {
	output   []byte
	block    bool
	lifetime time.Duration
	started  chan struct{}
	retry    bool
}

func (a *fakeAuth) login(ctx context.Context, root string, emit func(deviceChallenge) error) ([]byte, error) {
	d := a.lifetime
	if d == 0 {
		d = time.Minute
	}
	if err := emit(deviceChallenge{uri: "https://login.dingtalk.com/device", code: "TEST", lifetime: d}); err != nil {
		return nil, err
	}
	if a.retry {
		if err := emit(deviceChallenge{code: "NEXT"}); err != nil {
			return nil, err
		}
		if err := emit(deviceChallenge{uri: "https://login.dingtalk.com/next#authorize", code: "NEXT", lifetime: time.Minute}); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
	}
	if a.started != nil {
		close(a.started)
	}
	if a.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return append([]byte(nil), a.output...), nil
}
func setupLink(t *testing.T) (*Service, *linkRepo, *fakeAuth) {
	s, r, x := testService(t)
	repo := &linkRepo{fakeRepo: r}
	s.repo = repo
	x.pages = [][]byte{[]byte(`{"success":true,"authenticated":true,"token_valid":true,"corp_id":"corp","user_id":"user"}`)}
	a := &fakeAuth{output: []byte(`{"success":true,"token_valid":true,"corp_id":"corp","user_id":"user"}`)}
	s.authRunner = a
	if err := s.startLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.CloseLinks(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	return s, repo, a
}
func waitLink(t *testing.T, s *Service, scope model.DWSScope, id string) LinkChallenge {
	t.Helper()
	s.linksMu.Lock()
	l := s.links[id]
	s.linksMu.Unlock()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
		t.Fatal("link did not finish")
	}
	c, e := s.LinkStatus(context.Background(), scope, id)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestLinkFailureDiagnosis(t *testing.T) {
	p := &deviceParser{}
	_ = p.line("检查组织 CLI 授权状态...")
	if p.failureCode != "" {
		t.Fatal("progress is not a denial")
	}
	_ = p.line("⚠️  您暂无 CLI 数据访问权限")
	if safeCode(errors.New(p.failureCode)) != "dws_org_denied" {
		t.Fatal("expected safe organization denial code")
	}
}

func TestLinkAuthorizationAndIdentity(t *testing.T) {
	for _, mode := range []string{"success", "wrong_identity", "lease_lost"} {
		t.Run(mode, func(t *testing.T) {
			s, r, a := setupLink(t)
			if mode == "wrong_identity" {
				a.output = []byte(strings.Replace(string(a.output), `"user"`, `"other"`, 1))
			}
			if mode == "lease_lost" {
				r.failSave = true
			}
			c, e := s.Link(context.Background(), r.scope)
			if e != nil {
				t.Fatal(e)
			}
			result := waitLink(t, s, r.scope, c.ID)
			r.mu.Lock()
			defer r.mu.Unlock()
			if mode == "success" {
				if result.Status != "authorized" || r.saves != 1 || r.state != "active" {
					t.Fatal("authorization not persisted")
				}
			} else if result.Status != "failed" || r.saves != 0 {
				t.Fatal("invalid identity or lease persisted")
			}
			if result.UserCode != "" || result.VerificationURI != "" {
				t.Fatal("terminal challenge retained secrets")
			}
		})
	}
}
func TestLinkCancelOwnerTimeoutAndDisconnect(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "disconnect", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			s, r, a := setupLink(t)
			a.block = true
			a.started = make(chan struct{})
			if mode == "timeout" {
				a.lifetime = 10 * time.Millisecond
			}
			request, cancel := context.WithCancel(context.Background())
			c, e := s.Link(request, r.scope)
			if e != nil {
				t.Fatal(e)
			}
			cancel()
			<-a.started
			other := r.scope
			other.UserID = "other"
			if _, e = s.LinkStatus(context.Background(), other, c.ID); !errors.Is(e, model.ErrNotFound) {
				t.Fatal("owner leak")
			}
			if _, e = s.CancelLink(context.Background(), other, c.ID); !errors.Is(e, model.ErrNotFound) {
				t.Fatal("owner cancel")
			}
			if _, e = s.Link(context.Background(), r.scope); !errors.Is(e, model.ErrConflict) {
				t.Fatal("multiple active links")
			}
			e = nil
			switch mode {
			case "cancel":
				_, e = s.CancelLink(context.Background(), r.scope, c.ID)
			case "disconnect":
				e = s.Disconnect(context.Background(), r.scope)
			case "shutdown":
				s.linksCancel()
			}
			if e != nil {
				t.Fatal(e)
			}
			result := waitLink(t, s, r.scope, c.ID)
			expected := "cancelled"
			if mode == "timeout" {
				expected = "expired"
			}
			if result.Status != expected {
				t.Fatalf("status %s", result.Status)
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.saves != 0 {
				t.Fatal("cancel persisted workspace")
			}
		})
	}
}
func TestOfficialRetryReplacesDeadline(t *testing.T) {
	s, r, a := setupLink(t)
	a.retry = true
	a.lifetime = 10 * time.Millisecond
	c, e := s.Link(context.Background(), r.scope)
	if e != nil {
		t.Fatal(e)
	}
	result := waitLink(t, s, r.scope, c.ID)
	if result.Status != "authorized" || time.Until(result.ExpiresAt) < 50*time.Second {
		t.Fatalf("retry inherited previous expiry: %s", result.Status)
	}
	if !officialDeviceURL("https://login.dingtalk.com/next#authorize") {
		t.Fatal("official fragment rejected")
	}
}
func TestLinkNewAutomaticIdentity(t *testing.T) {
	for _, mode := range []string{"success", "missing_corp", "missing_user", "invalid_profile", "status_mismatch", "atomic_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, r, a := setupLink(t)
			r.empty = true
			switch mode {
			case "missing_corp":
				a.output = []byte(`{"success":true,"token_valid":true,"user_id":"user"}`)
			case "missing_user":
				a.output = []byte(`{"success":true,"token_valid":true,"corp_id":"corp"}`)
			case "invalid_profile":
				a.output = []byte(`{"success":true,"token_valid":true,"corp_id":"corp:other","user_id":"user"}`)
			case "status_mismatch":
				s.runner.(*fakeExec).pages = [][]byte{[]byte(`{"success":true,"authenticated":true,"token_valid":true,"corp_id":"corp","user_id":"other"}`)}
			case "atomic_failure":
				r.failSave = true
			}
			request := httptest.NewRequest(http.MethodPost, "/link", nil)
			request = request.WithContext(httpkit.WithUser(request.Context(), model.User{ID: r.scope.UserID}))
			w := httptest.NewRecorder()
			s.Routes().ServeHTTP(w, request)
			var challenge LinkChallenge
			if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &challenge) != nil || challenge.ConnectionID != r.scope.ConnectionID {
				t.Fatalf("unexpected API response %d %s", w.Code, w.Body.String())
			}
			result := waitLink(t, s, r.scope, challenge.ID)
			r.mu.Lock()
			defer r.mu.Unlock()
			if mode == "success" {
				if result.Status != "authorized" || r.state != "active" || r.identity.Profile != "corp:user" || len(r.workspace) == 0 {
					t.Fatal("identity and credential not atomically bound")
				}
				if arg(s.runner.(*fakeExec).calls[0], "--profile") != "corp:user" {
					t.Fatal("readonly status used scope ID")
				}
			} else if result.Status != "failed" || r.state != "" || r.identity.Profile != "" || len(r.workspace) != 0 {
				t.Fatal("failed link persisted partial identity or credentials")
			}
		})
	}
}
func TestLinkNewAdmissionBeforeCreation(t *testing.T) {
	s, r, a := setupLink(t)
	r.empty = true
	a.block = true
	a.started = make(chan struct{})
	c, err := s.LinkNew(context.Background(), r.scope.UserID)
	if err != nil {
		t.Fatal(err)
	}
	<-a.started
	if _, err = s.LinkNew(context.Background(), r.scope.UserID); !errors.Is(err, model.ErrConflict) {
		t.Fatal("duplicate admitted", err)
	}
	if r.creates != 1 {
		t.Fatal("admission failure created a row")
	}
	if _, err = s.CancelLink(context.Background(), r.scope, c.ID); err != nil {
		t.Fatal(err)
	}
	a.started = nil
	c, err = s.LinkNew(context.Background(), r.scope.UserID)
	if err != nil || c.ConnectionID != r.scope.ConnectionID {
		t.Fatal("cancelled pending not retryable", err)
	}
}
func TestLinkNewRejectsBodyAndMissingSession(t *testing.T) {
	s, r, _ := setupLink(t)
	for _, authenticated := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/link", strings.NewReader(`{"corp_id":"injected"}`))
		want := http.StatusUnauthorized
		if authenticated {
			req = req.WithContext(httpkit.WithUser(req.Context(), model.User{ID: r.scope.UserID}))
			want = http.StatusBadRequest
		}
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, req)
		if w.Code != want || r.creates != 0 {
			t.Fatal("invalid new-link admitted", w.Code)
		}
	}
}
func TestLinkRequiresRun(t *testing.T) {
	s, r, _ := testService(t)
	s.repo = &linkRepo{fakeRepo: r}
	if _, e := s.Link(context.Background(), r.scope); !errors.Is(e, model.ErrConflict) {
		t.Fatal(e)
	}
}
