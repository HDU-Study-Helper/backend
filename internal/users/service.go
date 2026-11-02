// Package users implements cookie sessions, user administration and the admin web UI.
package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"golang.org/x/crypto/bcrypt"
)

const SessionCookie = "academic_session"
const CSRFCookie = "academic_csrf"

type Config struct {
	SessionTTL    time.Duration
	SecureCookies bool
	Origin        string
	BcryptCost    int
	LoginLimit    int
	LoginWindow   time.Duration
}
type bucket struct {
	count int
	until time.Time
}
type Service struct {
	repo     model.UserRepository
	cfg      Config
	dummy    []byte
	mu       sync.Mutex
	attempts map[string]bucket
}

func New(repo model.UserRepository, cfg Config) (*Service, error) {
	if repo == nil {
		return nil, errors.New("users: repository required")
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 24 * time.Hour
	}
	if cfg.SessionTTL < time.Minute || cfg.SessionTTL > 30*24*time.Hour {
		return nil, errors.New("users: invalid session TTL")
	}
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	if cfg.BcryptCost < bcrypt.MinCost || cfg.BcryptCost > 14 {
		return nil, errors.New("users: invalid bcrypt cost")
	}
	if cfg.LoginLimit == 0 {
		cfg.LoginLimit = 10
	}
	if cfg.LoginWindow == 0 {
		cfg.LoginWindow = time.Minute
	}
	if cfg.LoginLimit < 1 || cfg.LoginWindow < time.Second || cfg.LoginWindow > 10*time.Minute {
		return nil, errors.New("users: invalid login limiter")
	}
	if cfg.Origin != "" {
		u, err := url.Parse(cfg.Origin)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("users: invalid origin")
		}
		cfg.Origin = u.Scheme + "://" + u.Host
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("dummy-password-never-used-for-login"), cfg.BcryptCost)
	if err != nil {
		return nil, errors.New("users: password initialization failed")
	}
	return &Service{repo: repo, cfg: cfg, dummy: dummy, attempts: make(map[string]bucket)}, nil
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{2,31}$`)

func validUsername(v string) bool { return usernamePattern.MatchString(v) }
func validPassword(v string) bool { return utf8.ValidString(v) && len(v) >= 12 && len(v) <= 72 }
func validDisplay(v string) bool {
	if !utf8.ValidString(v) || utf8.RuneCountInString(v) < 1 || utf8.RuneCountInString(v) > 80 || strings.TrimSpace(v) == "" {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validRole(r model.Role) bool { return r == model.RoleAdmin || r == model.RoleUser }
func (s *Service) hash(password string) (string, error) {
	b, e := bcrypt.GenerateFromPassword([]byte(password), s.cfg.BcryptCost)
	return string(b), e
}

// Bootstrap must be called explicitly with operator-provided credentials. The repository
// atomically prevents subsequent calls from adding or resetting an administrator.
func (s *Service) Bootstrap(ctx context.Context, username, password string) error {
	// Allow the explicitly requested local Demo bootstrap credential only here.
	// User creation and password changes retain the normal password policy.
	demoCredential := username == "admin" && password == "admin123"
	if !validUsername(username) || (!validPassword(password) && !demoCredential) {
		return model.ErrInvalidArgument
	}
	hash, err := s.hash(password)
	if err != nil {
		return errors.New("users: password hashing failed")
	}
	_, _, err = s.repo.BootstrapAdmin(ctx, model.User{Username: username, DisplayName: username, PasswordHash: hash, Role: model.RoleAdmin})
	return err
}
func token() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func tokenHash(v string) []byte { h := sha256.Sum256([]byte(v)); return h[:] }
func validToken(v string) bool {
	b, e := base64.RawURLEncoding.DecodeString(v)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == v
}
func (s *Service) cookies(w http.ResponseWriter, session, csrf string, clear bool) {
	age := int(s.cfg.SessionTTL.Seconds())
	expires := time.Now().Add(s.cfg.SessionTTL)
	if clear {
		age = -1
		expires = time.Unix(1, 0)
	}
	for _, c := range []*http.Cookie{{Name: SessionCookie, Value: session, HttpOnly: true}, {Name: CSRFCookie, Value: csrf}} {
		c.Path = "/"
		c.Secure = s.cfg.SecureCookies
		c.SameSite = http.SameSiteStrictMode
		c.MaxAge = age
		c.Expires = expires
		http.SetCookie(w, c)
	}
}
func (s *Service) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	expected := s.cfg.Origin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	return origin == expected
}
func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// Middleware authenticates each request against the repository. Context supplied by
// callers is never accepted as an authentication source.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		c, err := r.Cookie(SessionCookie)
		if err != nil || !validToken(c.Value) {
			httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
			return
		}
		u, err := s.repo.GetSessionUser(r.Context(), tokenHash(c.Value))
		if err != nil {
			if errors.Is(err, model.ErrNotFound) || errors.Is(err, model.ErrUnauthenticated) {
				httpkit.Error(w, r, 401, "unauthenticated", "登录已失效")
			} else {
				s.fail(w, r, err)
			}
			return
		}
		if u.Disabled || u.ID == "" {
			httpkit.Error(w, r, 401, "unauthenticated", "登录已失效")
			return
		}
		if !safeMethod(r.Method) {
			csrf, e := r.Cookie(CSRFCookie)
			header := r.Header.Get("X-CSRF-Token")
			if !s.sameOrigin(r) || e != nil || !validToken(csrf.Value) || subtle.ConstantTimeCompare([]byte(csrf.Value), []byte(header)) != 1 {
				httpkit.Error(w, r, 403, "forbidden", "请求来源或 CSRF 校验失败")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(httpkit.WithUser(r.Context(), u)))
	})
}
func (s *Service) allowLogin(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, v := range s.attempts {
		if !now.Before(v.until) {
			delete(s.attempts, key)
		}
	}
	b, exists := s.attempts[host]
	if !exists {
		if len(s.attempts) >= 4096 {
			return false
		}
		b.until = now.Add(s.cfg.LoginWindow)
	}
	if b.count >= s.cfg.LoginLimit {
		return false
	}
	b.count++
	s.attempts[host] = b
	return true
}
func (s *Service) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, model.ErrNotFound):
		httpkit.Error(w, r, 404, "not_found", "用户不存在")
	case errors.Is(err, model.ErrConflict), errors.Is(err, model.ErrLastAdmin):
		httpkit.Error(w, r, 409, "conflict", "用户名已存在或必须保留至少一名可用管理员")
	case errors.Is(err, model.ErrInvalidArgument):
		httpkit.Error(w, r, 400, "invalid_argument", "用户参数无效")
	default:
		httpkit.Error(w, r, 500, "internal", "服务暂不可用")
	}
}
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	u, ok := httpkit.CurrentUser(r.Context())
	if !ok || u.Role != model.RoleAdmin {
		httpkit.Error(w, r, 403, "forbidden", "仅管理员可操作用户")
		return false
	}
	return true
}
func invalid(w http.ResponseWriter, r *http.Request) {
	httpkit.Error(w, r, 400, "invalid_argument", "用户名须为3–32位ASCII字母数字及_.-；显示名1–80字；密码12–72字节；角色须为admin或user")
}
