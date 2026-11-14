package chaoxing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

func refreshFixtureSession(extra ...cookieEntry) contract.Session {
	s := state{Version: 1, Scope: testScope, Authorized: true, Cookies: []cookieEntry{
		{Origin: passport + "/login", Cookie: http.Cookie{Name: "_uid", Value: "fixture-account", Domain: ".chaoxing.com", Path: "/", Secure: true}},
		{Origin: passport + "/login", Cookie: http.Cookie{Name: "cx_p_token", Value: "fixture-root", Domain: ".chaoxing.com", Path: "/", Secure: true}},
	}}
	s.Cookies = append(s.Cookies, extra...)
	b, _ := json.Marshal(s)
	return b
}

func refreshAdapter(t *testing.T, f func(*http.Request) (*http.Response, error)) *adapter {
	t.Helper()
	a, err := New(contract.Dependencies{HTTPClient: &http.Client{Transport: roundTripFunc(f)}})
	if err != nil {
		t.Fatal(err)
	}
	a.(*adapter).client.Transport.(*pacedTransport).interval = 0
	return a.(*adapter)
}

func refreshResponse(r *http.Request, status int, body string, h http.Header) *http.Response {
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestRefreshSessionExchangesExistingCookieAndVerifiesCourses(t *testing.T) {
	var targets []string
	a := refreshAdapter(t, func(r *http.Request) (*http.Response, error) {
		targets = append(targets, r.URL.Hostname()+r.URL.Path)
		if r.Method != http.MethodGet {
			t.Fatal("refresh attempted a non-GET request")
		}
		if _, err := r.Cookie("k8s"); err == nil {
			t.Fatal("persisted routing ticket replayed")
		}
		if cookie, err := r.Cookie("cx_p_token"); err != nil || cookie.Value != "fixture-root" {
			t.Fatal("missing existing root login")
		}
		switch r.URL.Hostname() + r.URL.Path {
		case "i.chaoxing.com":
			return refreshResponse(r, 302, "", http.Header{"Location": {"https://i.chaoxing.com/base?t=1234"}}), nil
		case "i.chaoxing.com/base":
			return refreshResponse(r, 200, "<html>Home</html>", nil), nil
		case "passport2.chaoxing.com/login":
			return refreshResponse(r, 302, "", http.Header{"Location": {refreshHome + "/base"}, "Set-Cookie": {"vc3=fixture-refreshed; Domain=.chaoxing.com; Path=/; Secure; Max-Age=3600", "obsolete=; Domain=.chaoxing.com; Path=/; Max-Age=0"}}), nil
		case "mooc1-api.chaoxing.com/mycourse/backclazzdata":
			if cookie, err := r.Cookie("vc3"); err != nil || cookie.Value != "fixture-refreshed" {
				t.Fatal("exchange cookie not used for course verification")
			}
			return refreshResponse(r, 200, coursesFixture(0), nil), nil
		default:
			t.Fatal("unexpected request")
			return nil, contract.ErrUpstream
		}
	})
	extra := []cookieEntry{
		{Origin: passport, Cookie: http.Cookie{Name: "k8s", Value: "old", Path: "/"}},
		{Origin: passport, Cookie: http.Cookie{Name: "obsolete", Value: "old", Domain: ".chaoxing.com", Path: "/"}},
	}
	out, err := a.RefreshSession(context.Background(), testScope, refreshFixtureSession(extra...))
	if err != nil || len(targets) != 5 {
		t.Fatalf("refresh err=%v targets=%v", err, targets)
	}
	s, c, j, err := a.restore(testScope, out)
	if err != nil || !s.Authorized || !hasIdentity(j) {
		t.Fatal("result not restorable", err)
	}
	u, _ := url.Parse(api)
	for _, cookie := range c.Jar.Cookies(u) {
		if cookie.Name == "obsolete" || cookie.Name == "k8s" {
			t.Fatal("deleted or transient cookie survived")
		}
	}
	for _, entry := range s.Cookies {
		if entry.Cookie.Name == "vc3" && entry.Cookie.Expires.IsZero() {
			t.Fatal("new cookie expiry was not persisted")
		}
		if entry.Cookie.Name == "k8s" {
			t.Fatal("routing ticket persisted")
		}
	}
}

func TestRefreshSessionDoesNotTreatHomeOrLoginAsAuthorized(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"password form", `<html><input type='password'></html>`, 200, contract.ErrAuthRequired},
		{"login form", `<form id="loginForm"></form>`, 200, contract.ErrAuthRequired},
		{"home page", `<html>Home</html>`, 200, contract.ErrParse},
		{"malformed", `{`, 200, contract.ErrParse},
		{"unauthorized", ``, 401, contract.ErrAuthRequired},
		{"rate limit", ``, 429, contract.ErrRateLimited},
		{"verification", `操作异常，请输入图片中的验证码`, 200, contract.ErrVerificationRequired},
		{"upstream", ``, 500, contract.ErrUpstream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := refreshAdapter(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Hostname() == "mooc1-api.chaoxing.com" {
					return refreshResponse(r, tc.status, tc.body, nil), nil
				}
				return refreshResponse(r, 200, `<form><input type=password></form>`, nil), nil
			})
			out, err := a.RefreshSession(context.Background(), testScope, refreshFixtureSession())
			if !errors.Is(err, tc.want) || len(out) != 0 {
				t.Fatal("unexpected refresh result", err)
			}
		})
	}
}

func TestRefreshSessionRejectsScopePendingAndChangedAccounts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   contract.Scope
		session contract.Session
	}{
		{"pending", testScope, nil},
		{"other user", contract.Scope{UserID: "other", ConnectionID: testScope.ConnectionID}, refreshFixtureSession()},
		{"other connection", contract.Scope{UserID: testScope.UserID, ConnectionID: "other"}, refreshFixtureSession()},
		{"conflicting old identity", testScope, refreshFixtureSession(cookieEntry{Origin: api, Cookie: http.Cookie{Name: "UID", Value: "other", Path: "/"}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := refreshAdapter(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected request"); return nil, nil })
			if _, err := a.RefreshSession(context.Background(), tc.scope, tc.session); !errors.Is(err, contract.ErrAuthRequired) {
				t.Fatal(err)
			}
		})
	}
	for _, host := range []string{"i.chaoxing.com", "passport2.chaoxing.com", "mooc1-api.chaoxing.com"} {
		t.Run("account changed at "+host, func(t *testing.T) {
			a := refreshAdapter(t, func(r *http.Request) (*http.Response, error) {
				h := make(http.Header)
				if r.URL.Hostname() == host {
					h.Add("Set-Cookie", "_uid=other-account; Domain=.chaoxing.com; Path=/; Secure")
				}
				return refreshResponse(r, 200, coursesFixture(0), h), nil
			})
			out, err := a.RefreshSession(context.Background(), testScope, refreshFixtureSession())
			if !errors.Is(err, contract.ErrAuthRequired) || len(out) != 0 {
				t.Fatal("account changed", err)
			}
		})
	}
}

func TestRefreshRedirectAllowlist(t *testing.T) {
	for _, target := range []string{
		"http://i.chaoxing.com/base", "https://evil.example/", "https://public.chaoxing.com/", "https://passport2.chaoxing.com/logout",
		"https://i.chaoxing.com/base?url=https%3A%2F%2Fevil.example", "https://i.chaoxing.com/base?t=1&t=2", "https://i.chaoxing.com/base?t=x",
		"https://passport2.chaoxing.com/login?refer=https%3A%2F%2Fevil.example", "https://passport2.chaoxing.com/login?fid=-1&refer=https%3A%2F%2Fi.chaoxing.com&url=https%3A%2F%2Fevil.example",
		"https://user@i.chaoxing.com/base", "https://i.chaoxing.com:444/base", "https://i.chaoxing.com/base#fragment", "https://i.chaoxing.com/%62ase",
		"https://passport2.chaoxing.com/createqr", "https://i.chaoxing.com/base/settings",
	} {
		t.Run(target, func(t *testing.T) {
			requests := 0
			a := refreshAdapter(t, func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Hostname() == "mooc1-api.chaoxing.com" {
					return refreshResponse(r, 401, "", nil), nil
				}
				return refreshResponse(r, 302, "", http.Header{"Location": {target}}), nil
			})
			if _, err := a.RefreshSession(context.Background(), testScope, refreshFixtureSession()); !errors.Is(err, contract.ErrAuthRequired) || requests != 3 {
				t.Fatal("redirect followed or wrong result", requests, err)
			}
		})
	}
}

func TestRefreshSessionBoundsRedirectsAndPreservesErrors(t *testing.T) {
	requests := 0
	a := refreshAdapter(t, func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Hostname() == "mooc1-api.chaoxing.com" {
			return refreshResponse(r, 302, "", http.Header{"Location": {login}}), nil
		}
		return refreshResponse(r, 302, "", http.Header{"Location": {refreshHome}}), nil
	})
	if _, err := a.RefreshSession(context.Background(), testScope, refreshFixtureSession()); !errors.Is(err, contract.ErrAuthRequired) || requests != 9 {
		t.Fatal("redirect budget or course redirect verification", requests, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.RefreshSession(ctx, testScope, refreshFixtureSession()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a = refreshAdapter(t, func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })
	if _, err := a.RefreshSession(context.Background(), testScope, refreshFixtureSession()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestRefreshCookieMaxAgePrecedesExpiresAndDoesNotExtendOnRestore(t *testing.T) {
	a := refreshAdapter(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected request"); return nil, nil })
	s, _, j, _ := a.restore(testScope, refreshFixtureSession())
	u, _ := url.Parse(passport)
	j.SetCookies(u, []*http.Cookie{{Name: "precedence", Value: "fixture", Path: "/", MaxAge: 60, Expires: time.Now().Add(-time.Hour)}})
	var expiry time.Time
	for _, entry := range j.Entries {
		if entry.Cookie.Name == "precedence" {
			expiry = entry.Cookie.Expires
		}
	}
	if time.Until(expiry) < 59*time.Second || time.Until(expiry) > 61*time.Second {
		t.Fatal("Max-Age did not override Expires")
	}
	_, _, restored, err := a.restore(testScope, save(s, j))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range restored.Entries {
		if entry.Cookie.Name == "precedence" && (!entry.Cookie.Expires.Equal(expiry) || entry.Cookie.MaxAge != 0) {
			t.Fatal("restore changed absolute lifetime")
		}
	}
}
