package users

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConcurrentPasswordResetCannotIssueSession(t *testing.T) {
	s, repo := fixture(t)
	repo.beforeSession = func() { u := repo.users["0002"]; u.PasswordHash = "changed-by-other-request"; repo.users[u.ID] = u }
	expectCode(t, request(s, "POST", "/auth/login", `{"username":"student","password":"user-password-123"}`, nil, false), 401)
	if len(repo.sessions) != 0 {
		t.Fatal("session issued after concurrent password reset")
	}
}
func TestExpiryAndAdminReset(t *testing.T) {
	s, repo := fixture(t)
	admin := loginAs(t, s, "root.admin", "correct-password-123")
	student := loginAs(t, s, "student", "user-password-123")
	expectCode(t, request(s, "PUT", "/users/0002", `{"password":"reset-password-123"}`, admin, true), 200)
	expectCode(t, request(s, "GET", "/auth/me", "", student, false), 401)
	student = loginAs(t, s, "student", "reset-password-123")
	for k, v := range repo.sessions {
		if v.UserID == "0002" {
			v.ExpiresAt = time.Now().Add(-time.Second)
			repo.sessions[k] = v
		}
	}
	expectCode(t, request(s, "GET", "/auth/me", "", student, false), 401)
}
func TestCrossOriginAuthenticatedWrite(t *testing.T) {
	s, _ := fixture(t)
	cookies := loginAs(t, s, "root.admin", "correct-password-123")
	// A valid double-submit pair still requires the exact origin.
	r := newOriginRequest(cookies)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	expectCode(t, w, 403)
}
func newOriginRequest(cookies []*http.Cookie) *http.Request {
	r := httptest.NewRequest("POST", "https://example.test/auth/logout", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://evil.test")
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
		if c.Name == CSRFCookie {
			r.Header.Set("X-CSRF-Token", c.Value)
		}
	}
	return r
}
