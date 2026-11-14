package mooc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

func TestRefreshSessionVerifiesLoginAndPersistsRedirectCookies(t *testing.T) {
	calls := []string{}
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		if r.URL.Scheme != "https" || r.URL.Host != "www.icourse163.org" {
			t.Fatal("unexpected credential destination")
		}
		switch r.URL.Path {
		case "/home.htm":
			if r.Method != http.MethodGet {
				t.Fatal("home must be read only")
			}
			if cookie, err := r.Cookie("long_login"); err != nil || cookie.Value != "fixture-login" {
				t.Fatal("existing login cookie missing")
			}
			res := response("")
			res.StatusCode = http.StatusFound
			res.Header.Set("Location", origin+"/passport/logingate/changeCookie.htm?private=fixture")
			return res, nil
		case "/passport/logingate/changeCookie.htm":
			return response("home", "NTESSTUDYSI=new-csrf; Path=/; Secure; Max-Age=60", "STUDY_INFO=updated; Path=/; Secure", "obsolete=; Path=/; Max-Age=0"), nil
		case "/web/j/learnerCourseRpcBean.getMyLearnedCoursePanelList.rpc":
			if r.Method != http.MethodPost || r.URL.Query().Get("csrfKey") != "new-csrf" || r.Header.Get("edu-script-token") != "new-csrf" || r.Header.Get("Referer") != origin+"/" {
				t.Fatal("verification did not use refreshed request credentials")
			}
			if err := r.ParseForm(); err != nil || r.PostForm.Encode() != "courseType=1&p=1&psize=8&type=30" {
				t.Fatal("invalid official learner list parameters")
			}
			if _, err := r.Cookie("obsolete"); err == nil {
				t.Fatal("deleted cookie was sent")
			}
			// A normal empty course array proves login without claiming any course.
			return response(`{"code":0,"result":{"result":[],"pagination":{"totlePageCount":1}}}`), nil
		default:
			t.Fatal("unexpected request", r.URL.Path)
			return nil, nil
		}
	})
	s, h, err := a.restore(scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(origin)
	h.Jar.SetCookies(u, []*http.Cookie{{Name: "long_login", Value: "fixture-login", Path: "/", Expires: time.Now().Add(time.Hour)}, {Name: "obsolete", Value: "old", Path: "/"}})
	// The local flag may be stale; only the upstream verification grants success.
	s.Authorized = false
	s.PollKey, s.QRURL = "old-challenge", "old-qr"
	before, _ := save(s, h)
	after, err := a.RefreshSession(context.Background(), scope, before)
	if err != nil || len(calls) != 3 {
		t.Fatal(err, calls)
	}
	st, restored, err := a.restore(scope, after)
	if err != nil || !st.Authorized || st.Scope != scope || st.PollKey != "" || st.QRURL != "" {
		t.Fatal("invalid refreshed session state", err)
	}
	cookies := map[string]string{}
	for _, cookie := range restored.Jar.Cookies(u) {
		cookies[cookie.Name] = cookie.Value
	}
	if cookies["STUDY_INFO"] != "updated" || cookies["NTESSTUDYSI"] != "new-csrf" || cookies["long_login"] != "fixture-login" || cookies["obsolete"] != "" {
		t.Fatal("cookie exchange or deletion not persisted")
	}
	// The caller's encrypted-session input is never mutated, including on failure.
	var original state
	if json.Unmarshal(before, &original) != nil || original.Authorized || original.PollKey != "old-challenge" {
		t.Fatal("input session mutated")
	}
}

func TestRefreshSessionErrorClassificationAndBoundedHandshake(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		homeStatus, rpcStatus int
		body                  string
		want                  error
		calls                 int
	}{
		{"persistent stale login", 200, 200, staleCoursePanels, contract.ErrAuthRequired, 2},
		{"authentication code", 200, 200, `{"code":1,"result":{}}`, contract.ErrAuthRequired, 2},
		{"expired authentication code", 200, 200, `{"code":-1007,"result":{}}`, contract.ErrAuthRequired, 2},
		{"forbidden list", 200, 403, "forbidden", contract.ErrAuthRequired, 2},
		{"unauthorized home", 401, 200, "", contract.ErrAuthRequired, 1},
		{"rate limited home", 429, 200, "", contract.ErrRateLimited, 1},
		{"rate limited list", 200, 429, "", contract.ErrRateLimited, 2},
		{"transient home", 503, 200, "", errTransientRead, 1},
		{"malformed verification", 200, 200, "<html>login</html>", contract.ErrParse, 2},
		{"unknown null", 200, 200, `{"code":0,"result":{"result":null}}`, contract.ErrParse, 2},
		{"missing list", 200, 200, `{"code":0,"result":{}}`, contract.ErrParse, 2},
		{"invalid course", 200, 200, `{"code":0,"result":{"result":[{}]}}`, contract.ErrParse, 2},
		{"upstream failure", 200, 500, "", contract.ErrUpstream, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := testAdapter(func(r *http.Request) (*http.Response, error) {
				calls++
				res := response(tc.body, "STUDY_INFO=changed-but-unverified; Path=/")
				res.StatusCode = tc.rpcStatus
				if r.URL.Path == "/home.htm" {
					res.StatusCode = tc.homeStatus
				}
				return res, nil
			})
			result, err := a.RefreshSession(context.Background(), scope, sessionFor(t, a, scope, "csrf"))
			if !errors.Is(err, tc.want) || result != nil || calls != tc.calls {
				t.Fatal(err, calls, "session returned:", result != nil)
			}
		})
	}
}

func TestRefreshSessionValidatesFullScopeAndCancellationBeforeNetwork(t *testing.T) {
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid scope or cancellation made a network request")
		return nil, nil
	})
	session := sessionFor(t, a, scope, "csrf")
	for _, other := range []contract.Scope{{UserID: "other", ConnectionID: scope.ConnectionID}, {UserID: scope.UserID, ConnectionID: "other"}, {}} {
		if result, err := a.RefreshSession(context.Background(), other, session); !errors.Is(err, contract.ErrInvalid) || result != nil {
			t.Fatal("scope mismatch accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.RefreshSession(ctx, scope, session); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRefreshSessionCannotTrustAuthorizedFlagWithoutVerification(t *testing.T) {
	calls := 0
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		calls++
		return response("home without login"), nil
	})
	s, h, _ := a.restore(scope, nil)
	s.Authorized = true
	input, _ := save(s, h)
	result, err := a.RefreshSession(context.Background(), scope, input)
	if !errors.Is(err, contract.ErrAuthRequired) || result != nil || calls != 1 {
		t.Fatal("local flag must not prove authorization", err, calls)
	}
}

func TestRefreshSessionRejectsRedirectsOutsideOfficialHTTPSOrigin(t *testing.T) {
	for _, destination := range []string{"https://untrusted.invalid/", "http://www.icourse163.org/home.htm"} {
		t.Run(destination, func(t *testing.T) {
			calls := 0
			a := testAdapter(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Scheme != "https" || r.URL.Host != "www.icourse163.org" {
					t.Fatal("credentials followed an untrusted redirect")
				}
				res := response("")
				res.StatusCode = http.StatusFound
				res.Header.Set("Location", destination)
				return res, nil
			})
			result, err := a.RefreshSession(context.Background(), scope, sessionFor(t, a, scope, "csrf"))
			if err == nil || result != nil || calls != 1 || strings.Contains(err.Error(), "csrf") || strings.Contains(err.Error(), "private=") {
				t.Fatal("redirect boundary failure", err, calls)
			}
		})
	}
}

func TestSavedCookieLifetimeAndDeletionSurviveRestore(t *testing.T) {
	a := testAdapter(nil)
	s, h, _ := a.restore(scope, nil)
	u, _ := url.Parse(origin)
	h.Jar.SetCookies(u, []*http.Cookie{{Name: "bounded", Value: "fixture", Path: "/", MaxAge: 60}})
	first, _ := save(s, h)
	var saved state
	if err := json.Unmarshal(first, &saved); err != nil || len(saved.Cookies) != 1 || saved.Cookies[0].Cookie.MaxAge != 0 || saved.Cookies[0].Cookie.Expires.IsZero() {
		t.Fatal("MaxAge must be persisted as an absolute expiry", err)
	}
	expiry := saved.Cookies[0].Cookie.Expires
	for i := 0; i < 3; i++ {
		s, h, _ = a.restore(scope, first)
		first, _ = save(s, h)
		if json.Unmarshal(first, &saved) != nil || !saved.Cookies[0].Cookie.Expires.Equal(expiry) {
			t.Fatal("restore extended cookie expiry")
		}
	}
	// Expiry is honored without resetting a relative MaxAge at restore time.
	saved.Cookies[0].Cookie.Expires = time.Now().Add(-time.Hour)
	expired, _ := json.Marshal(saved)
	s, h, _ = a.restore(scope, expired)
	if len(h.Jar.Cookies(u)) != 0 {
		t.Fatal("expired cookie revived")
	}
	h.Jar.SetCookies(u, []*http.Cookie{{Name: "bounded", Value: "new", Path: "/"}})
	h.Jar.SetCookies(u, []*http.Cookie{{Name: "bounded", Path: "/", MaxAge: -1}})
	deleted, _ := save(s, h)
	_, h, _ = a.restore(scope, deleted)
	if len(h.Jar.Cookies(u)) != 0 {
		t.Fatal("deleted cookie revived")
	}
}

func TestSyncAuthErrorLeavesSingleRetryToHost(t *testing.T) {
	calls := 0
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path == "/home.htm" {
			t.Fatal("Sync must leave authentication error recovery to the host")
		}
		res := response("forbidden")
		res.StatusCode = http.StatusForbidden
		return res, nil
	})
	page, err := a.Sync(context.Background(), scope, sessionFor(t, a, scope, "csrf"), contract.Course, "")
	if !errors.Is(err, contract.ErrAuthRequired) || calls != 1 || page.NextCursor != "" || page.Session != nil {
		t.Fatal("authentication error was retried or advanced", err, calls)
	}
}
