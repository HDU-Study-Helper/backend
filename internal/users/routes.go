package users

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

// Routes is mounted beneath /api/v1. Only login is public.
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/auth/login", s.login)
	r.Group(func(r chi.Router) {
		r.Use(s.Middleware)
		r.Get("/auth/me", s.me)
		r.Post("/auth/logout", s.logout)
		r.Post("/auth/password", s.password)
		r.Get("/users", s.list)
		r.Post("/users", s.create)
		r.Get("/users/{id}", s.get)
		r.Put("/users/{id}", s.update)
		r.Delete("/users/{id}", s.delete)
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { httpkit.Error(w, r, 404, "not_found", "接口不存在") })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpkit.Error(w, r, 405, "method_not_allowed", "请求方法不支持")
	})
	return r
}
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.sameOrigin(r) {
		httpkit.Error(w, r, 403, "forbidden", "请求来源校验失败")
		return
	}
	if !s.allowLogin(r) {
		w.Header().Set("Retry-After", strconv.Itoa(int(s.cfg.LoginWindow.Seconds())))
		httpkit.Error(w, r, 429, "rate_limited", "登录请求过多，请稍后重试")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if httpkit.DecodeJSON(w, r, &in) != nil {
		return
	}
	// Every decoded login performs bcrypt, including unknown users and invalid input.
	u, err := s.repo.GetByUsername(r.Context(), in.Username)
	hash := s.dummy
	if err == nil && u.PasswordHash != "" {
		hash = []byte(u.PasswordHash)
	}
	compare := bcrypt.CompareHashAndPassword(hash, []byte(in.Password))
	if err != nil && !errors.Is(err, model.ErrNotFound) && !errors.Is(err, model.ErrUnauthenticated) {
		s.fail(w, r, err)
		return
	}
	if err != nil || compare != nil || u.Disabled || !validUsername(in.Username) || len(in.Password) == 0 || len(in.Password) > 72 {
		httpkit.Error(w, r, 401, "unauthenticated", "用户名或密码错误")
		return
	}
	session, e := token()
	if e != nil {
		s.fail(w, r, e)
		return
	}
	csrf, e := token()
	if e != nil {
		s.fail(w, r, e)
		return
	}
	now := time.Now().UTC()
	if e = s.repo.CreateSession(r.Context(), model.Session{Hash: tokenHash(session), UserID: u.ID, CreatedAt: now, ExpiresAt: now.Add(s.cfg.SessionTTL), ExpectedPasswordHash: u.PasswordHash}); e != nil {
		if errors.Is(e, model.ErrUnauthenticated) || errors.Is(e, model.ErrNotFound) {
			httpkit.Error(w, r, 401, "unauthenticated", "用户名或密码错误")
		} else {
			s.fail(w, r, e)
		}
		return
	}
	u, e = s.repo.GetSessionUser(r.Context(), tokenHash(session))
	if e != nil || u.Disabled {
		_ = s.repo.DeleteSession(r.Context(), tokenHash(session))
		if e == nil || errors.Is(e, model.ErrUnauthenticated) || errors.Is(e, model.ErrNotFound) {
			httpkit.Error(w, r, 401, "unauthenticated", "用户名或密码错误")
		} else {
			s.fail(w, r, e)
		}
		return
	}
	if old, e := r.Cookie(SessionCookie); e == nil && validToken(old.Value) {
		if e = s.repo.DeleteSession(r.Context(), tokenHash(old.Value)); e != nil {
			_ = s.repo.DeleteSession(r.Context(), tokenHash(session))
			s.fail(w, r, e)
			return
		}
	}
	s.cookies(w, session, csrf, false)
	httpkit.WriteJSON(w, 200, u)
}
func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	u, _ := httpkit.CurrentUser(r.Context())
	httpkit.WriteJSON(w, 200, u)
}
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie(SessionCookie)
	if err := s.repo.DeleteSession(r.Context(), tokenHash(c.Value)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.cookies(w, "", "", true)
	httpkit.WriteJSON(w, 204, nil)
}
func (s *Service) password(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Password        string `json:"password"`
	}
	if httpkit.DecodeJSON(w, r, &in) != nil {
		return
	}
	if !validPassword(in.Password) {
		invalid(w, r)
		return
	}
	actor, _ := httpkit.CurrentUser(r.Context())
	u, err := s.repo.GetByID(r.Context(), actor.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.CurrentPassword)) != nil {
		httpkit.Error(w, r, 401, "unauthenticated", "当前密码错误")
		return
	}
	hash, err := s.hash(in.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = s.repo.Update(r.Context(), actor.ID, model.UserUpdate{PasswordHash: &hash}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = s.repo.RevokeUserSessions(r.Context(), actor.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.cookies(w, "", "", true)
	httpkit.WriteJSON(w, 204, nil)
}
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 || v > 100 {
			invalid(w, r)
			return
		}
		limit = v
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 128 {
		invalid(w, r)
		return
	}
	items, err := s.repo.List(r.Context(), limit+1, cursor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	if items == nil {
		items = []model.User{}
	}
	httpkit.WriteJSON(w, 200, struct {
		Items []model.User `json:"items"`
		Next  string       `json:"next_cursor,omitempty"`
	}{items, next})
}
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	u, err := s.repo.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, u)
}
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in struct {
		Username    string     `json:"username"`
		DisplayName string     `json:"display_name"`
		Password    string     `json:"password"`
		Role        model.Role `json:"role"`
		Disabled    bool       `json:"disabled"`
	}
	if httpkit.DecodeJSON(w, r, &in) != nil {
		return
	}
	if !validUsername(in.Username) || !validDisplay(in.DisplayName) || !validPassword(in.Password) || !validRole(in.Role) {
		invalid(w, r)
		return
	}
	hash, err := s.hash(in.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.repo.Create(r.Context(), model.User{Username: in.Username, DisplayName: in.DisplayName, PasswordHash: hash, Role: in.Role, Disabled: in.Disabled})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 201, u)
}
func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in struct {
		DisplayName *string     `json:"display_name"`
		Password    *string     `json:"password"`
		Role        *model.Role `json:"role"`
		Disabled    *bool       `json:"disabled"`
	}
	if httpkit.DecodeJSON(w, r, &in) != nil {
		return
	}
	if (in.DisplayName == nil && in.Password == nil && in.Role == nil && in.Disabled == nil) || (in.DisplayName != nil && !validDisplay(*in.DisplayName)) || (in.Password != nil && !validPassword(*in.Password)) || (in.Role != nil && !validRole(*in.Role)) {
		invalid(w, r)
		return
	}
	patch := model.UserUpdate{DisplayName: in.DisplayName, Role: in.Role, Disabled: in.Disabled}
	if in.Password != nil {
		hash, err := s.hash(*in.Password)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		patch.PasswordHash = &hash
	}
	id := chi.URLParam(r, "id")
	u, err := s.repo.Update(r.Context(), id, patch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if patch.PasswordHash != nil || patch.Role != nil || patch.Disabled != nil {
		if err = s.repo.RevokeUserSessions(r.Context(), id); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	httpkit.WriteJSON(w, 200, u)
}
func (s *Service) delete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.repo.Delete(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 204, nil)
}
