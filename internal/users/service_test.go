package users

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"golang.org/x/crypto/bcrypt"
)

type memoryRepo struct {
	users         map[string]model.User
	sessions      map[string]model.Session
	booted        bool
	seq           int
	beforeSession func()
}

func newMemory() *memoryRepo {
	return &memoryRepo{users: map[string]model.User{}, sessions: map[string]model.Session{}}
}
func (m *memoryRepo) Create(_ context.Context, u model.User) (model.User, error) {
	for _, v := range m.users {
		if v.Username == u.Username {
			return model.User{}, model.ErrConflict
		}
	}
	m.seq++
	u.ID = fmt.Sprintf("%04d", m.seq)
	m.users[u.ID] = u
	return u, nil
}
func (m *memoryRepo) GetByID(_ context.Context, id string) (model.User, error) {
	u, ok := m.users[id]
	if !ok {
		return u, model.ErrNotFound
	}
	return u, nil
}
func (m *memoryRepo) GetByUsername(_ context.Context, name string) (model.User, error) {
	for _, u := range m.users {
		if u.Username == name {
			return u, nil
		}
	}
	return model.User{}, model.ErrNotFound
}
func (m *memoryRepo) List(_ context.Context, limit int, cursor string) ([]model.User, error) {
	var ids []string
	for id := range m.users {
		if id > cursor {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var out []model.User
	for _, id := range ids {
		if len(out) == limit {
			break
		}
		out = append(out, m.users[id])
	}
	return out, nil
}
func (m *memoryRepo) Update(ctx context.Context, id string, p model.UserUpdate) (model.User, error) {
	u, e := m.GetByID(ctx, id)
	if e != nil {
		return u, e
	}
	if p.DisplayName != nil {
		u.DisplayName = *p.DisplayName
	}
	if p.Role != nil {
		u.Role = *p.Role
	}
	if p.PasswordHash != nil {
		u.PasswordHash = *p.PasswordHash
	}
	if p.Disabled != nil {
		u.Disabled = *p.Disabled
	}
	m.users[id] = u
	if p.Role != nil || p.PasswordHash != nil || p.Disabled != nil {
		_ = m.RevokeUserSessions(ctx, id)
	}
	return u, nil
}
func (m *memoryRepo) Delete(ctx context.Context, id string) error {
	if _, e := m.GetByID(ctx, id); e != nil {
		return e
	}
	delete(m.users, id)
	return m.RevokeUserSessions(ctx, id)
}
func (m *memoryRepo) BootstrapAdmin(ctx context.Context, u model.User) (model.User, bool, error) {
	if m.booted || len(m.users) > 0 {
		return model.User{}, false, nil
	}
	m.booted = true
	u, e := m.Create(ctx, u)
	return u, e == nil, e
}
func (m *memoryRepo) CreateSession(_ context.Context, s model.Session) error {
	if m.beforeSession != nil {
		m.beforeSession()
	}
	u, ok := m.users[s.UserID]
	if !ok || u.Disabled || s.ExpectedPasswordHash == "" || s.ExpectedPasswordHash != u.PasswordHash {
		return model.ErrUnauthenticated
	}
	s.ExpectedPasswordHash = ""
	m.sessions[hex.EncodeToString(s.Hash)] = s
	return nil
}
func (m *memoryRepo) GetSessionUser(ctx context.Context, hash []byte) (model.User, error) {
	s, ok := m.sessions[hex.EncodeToString(hash)]
	if !ok || !time.Now().Before(s.ExpiresAt) {
		return model.User{}, model.ErrUnauthenticated
	}
	u, e := m.GetByID(ctx, s.UserID)
	if e != nil || u.Disabled {
		return model.User{}, model.ErrUnauthenticated
	}
	return u, nil
}
func (m *memoryRepo) DeleteSession(_ context.Context, hash []byte) error {
	delete(m.sessions, hex.EncodeToString(hash))
	return nil
}
func (m *memoryRepo) RevokeUserSessions(_ context.Context, id string) error {
	for k, s := range m.sessions {
		if s.UserID == id {
			delete(m.sessions, k)
		}
	}
	return nil
}
func fixture(t *testing.T) (*Service, *memoryRepo) {
	t.Helper()
	repo := newMemory()
	s, e := New(repo, Config{BcryptCost: bcrypt.MinCost, SecureCookies: true})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Bootstrap(context.Background(), "root.admin", "correct-password-123"); e != nil {
		t.Fatal(e)
	}
	hash, e := s.hash("user-password-123")
	if e != nil {
		t.Fatal(e)
	}
	_, e = repo.Create(context.Background(), model.User{Username: "student", DisplayName: "学生", PasswordHash: hash, Role: model.RoleUser})
	if e != nil {
		t.Fatal(e)
	}
	return s, repo
}
func request(s *Service, method, path, body string, cookies []*http.Cookie, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.1:1234"
	for _, c := range cookies {
		r.AddCookie(c)
		if csrf && c.Name == CSRFCookie {
			r.Header.Set("X-CSRF-Token", c.Value)
		}
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	return w
}
func loginAs(t *testing.T, s *Service, user, password string) []*http.Cookie {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"username": user, "password": password})
	w := request(s, "POST", "/auth/login", string(b), nil, false)
	if w.Code != 200 {
		t.Fatalf("login %d: %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()
}
func expectCode(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("want %d got %d: %s", code, w.Code, w.Body.String())
	}
}
func TestLoginSessionAndLogout(t *testing.T) {
	s, repo := fixture(t)
	cookies := loginAs(t, s, "root.admin", "correct-password-123")
	if len(cookies) != 2 {
		t.Fatalf("cookies %v", cookies)
	}
	for _, c := range cookies {
		if c.SameSite != http.SameSiteStrictMode || !c.Secure || c.Path != "/" {
			t.Fatal("unsafe cookie")
		}
		if c.Name == SessionCookie {
			if !c.HttpOnly || !validToken(c.Value) {
				t.Fatal("unsafe session cookie")
			}
			stored, ok := repo.sessions[hex.EncodeToString(tokenHash(c.Value))]
			if !ok || len(stored.Hash) != 32 || bytes.Contains(stored.Hash, []byte(c.Value)) {
				t.Fatal("session token stored raw")
			}
		}
	}
	w := request(s, "GET", "/auth/me", "", cookies, false)
	expectCode(t, w, 200)
	if strings.Contains(w.Body.String(), "$2") || strings.Contains(w.Body.String(), "password") {
		t.Fatal("hash leaked")
	}
	expectCode(t, request(s, "POST", "/auth/logout", "{}", cookies, true), 204)
	expectCode(t, request(s, "GET", "/auth/me", "", cookies, false), 401)
}
func TestIdentityAndPermission(t *testing.T) {
	s, repo := fixture(t)
	user := loginAs(t, s, "student", "user-password-123")
	for _, v := range []struct{ method, path, body string }{{"GET", "/users", ""}, {"GET", "/users/0001", ""}, {"POST", "/users", `{"username":"new-user"}`}, {"PUT", "/users/0001", `{"role":"user"}`}, {"DELETE", "/users/0001", ""}} {
		expectCode(t, request(s, v.method, v.path, v.body, user, true), 403)
	}
	// A fabricated trusted context cannot replace cookie authentication.
	r := httptest.NewRequest("GET", "https://example.test/auth/me", nil)
	r = r.WithContext(httpkit.WithUser(r.Context(), repo.users["0001"]))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	expectCode(t, w, 401)
	admin := loginAs(t, s, "root.admin", "correct-password-123")
	u := repo.users["0001"]
	u.Role = model.RoleUser
	repo.users[u.ID] = u
	expectCode(t, request(s, "GET", "/users", "", admin, false), 403)
	u.Disabled = true
	repo.users[u.ID] = u
	expectCode(t, request(s, "GET", "/auth/me", "", admin, false), 401)
}
func TestCSRFAndStrictLogin(t *testing.T) {
	s, _ := fixture(t)
	cookies := loginAs(t, s, "root.admin", "correct-password-123")
	expectCode(t, request(s, "POST", "/auth/logout", "{}", cookies, false), 403)
	for _, origin := range []string{"", "https://evil.test", "null"} {
		r := httptest.NewRequest("POST", "https://example.test/auth/login", strings.NewReader(`{"username":"root.admin","password":"correct-password-123"}`))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, r)
		expectCode(t, w, 403)
	}
	for _, body := range []string{`null`, `[]`, `{"username":"root.admin","password":"correct-password-123","actor":"admin"}`, `{} {}`, `{"password":"` + strings.Repeat("x", 17000) + `"}`} {
		expectCode(t, request(s, "POST", "/auth/login", body, nil, false), 400)
	}
	r := httptest.NewRequest("POST", "https://example.test/auth/login", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	expectCode(t, w, 415)
}
func TestUniformFailureAndRateLimit(t *testing.T) {
	s, _ := fixture(t)
	a := request(s, "POST", "/auth/login", `{"username":"root.admin","password":"wrong-password"}`, nil, false)
	b := request(s, "POST", "/auth/login", `{"username":"missing","password":"wrong-password"}`, nil, false)
	expectCode(t, a, 401)
	expectCode(t, b, 401)
	if a.Body.String() != b.Body.String() {
		t.Fatal("enumeration")
	}
	s.cfg.LoginLimit = 2
	r := httptest.NewRequest("POST", "https://example.test/auth/login", nil)
	r.RemoteAddr = "192.0.2.1:10000"
	r.Header.Set("X-Forwarded-For", "198.51.100.77")
	if s.allowLogin(r) {
		t.Fatal("XFF bypass")
	}
	if len(s.attempts) > 4096 {
		t.Fatal("unbounded limiter")
	}
	for i := 0; i < 4096; i++ {
		s.attempts[fmt.Sprint(i)] = bucket{until: time.Now().Add(time.Minute)}
	}
	r.RemoteAddr = "198.51.100.8:10000"
	if s.allowLogin(r) {
		t.Fatal("capacity bypass")
	}
}
func TestPasswordChangeIsSelfOnlyAndRevokesAll(t *testing.T) {
	s, repo := fixture(t)
	one := loginAs(t, s, "student", "user-password-123")
	two := loginAs(t, s, "student", "user-password-123")
	expectCode(t, request(s, "POST", "/auth/password", `{"current_password":"user-password-123","password":"new-password-123","user_id":"0001"}`, one, true), 400)
	expectCode(t, request(s, "POST", "/auth/password", `{"current_password":"wrong","password":"new-password-123"}`, one, true), 401)
	expectCode(t, request(s, "POST", "/auth/password", `{"current_password":"user-password-123","password":"new-password-123"}`, one, true), 204)
	expectCode(t, request(s, "GET", "/auth/me", "", two, false), 401)
	if bcrypt.CompareHashAndPassword([]byte(repo.users["0002"].PasswordHash), []byte("new-password-123")) != nil {
		t.Fatal("password not hashed")
	}
	loginAs(t, s, "root.admin", "correct-password-123")
	loginAs(t, s, "student", "new-password-123")
}
func TestAdminCRUDValidationPaginationAndRevocation(t *testing.T) {
	s, repo := fixture(t)
	admin := loginAs(t, s, "root.admin", "correct-password-123")
	student := loginAs(t, s, "student", "user-password-123")
	expectCode(t, request(s, "GET", "/users?limit=1", "", admin, false), 200)
	page := request(s, "GET", "/users?limit=1", "", admin, false)
	if !strings.Contains(page.Body.String(), `"next_cursor":"0001"`) {
		t.Fatal(page.Body.String())
	}
	expectCode(t, request(s, "GET", "/users?limit=101", "", admin, false), 400)
	for _, body := range []string{`{"username":"用户abc","display_name":"好","password":"good-password-123","role":"user"}`, `{"username":"abc","display_name":"好","password":"short","role":"user"}`, `{"username":"abc","display_name":"好","password":"good-password-123","role":"owner"}`, `{"username":"abc","display_name":"\n","password":"good-password-123","role":"user"}`} {
		expectCode(t, request(s, "POST", "/users", body, admin, true), 400)
	}
	expectCode(t, request(s, "POST", "/users", `{"username":"new-user","display_name":"<script>alert(1)</script>","password":"good-password-123","role":"user"}`, admin, true), 201)
	u, _ := repo.GetByUsername(context.Background(), "new-user")
	if u.PasswordHash == "good-password-123" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("good-password-123")) != nil {
		t.Fatal("password not hashed")
	}
	expectCode(t, request(s, "PUT", "/users/0002", `{"role":"admin"}`, admin, true), 200)
	expectCode(t, request(s, "GET", "/auth/me", "", student, false), 401)
	expectCode(t, request(s, "DELETE", "/users/"+u.ID, "", admin, true), 204)
	expectCode(t, request(s, "GET", "/users/"+u.ID, "", admin, false), 404)
}
func TestBootstrapAndWeb(t *testing.T) {
	s, repo := fixture(t)
	before := repo.users["0001"].PasswordHash
	if err := s.Bootstrap(context.Background(), "second-admin", "different-password"); err != nil {
		t.Fatal(err)
	}
	if len(repo.users) != 2 || repo.users["0001"].PasswordHash != before {
		t.Fatal("bootstrap not one-shot")
	}
	if err := s.Bootstrap(context.Background(), "bad", "short"); err == nil {
		t.Fatal("invalid bootstrap")
	}
	w := httptest.NewRecorder()
	s.Web().ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/admin", nil))
	expectCode(t, w, 200)
	if !strings.Contains(w.Body.String(), "用户管理") || strings.Contains(w.Body.String(), "innerHTML") {
		t.Fatal("unsafe or missing UI")
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing CSP")
	}
}
