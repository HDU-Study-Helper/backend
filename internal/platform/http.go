package platform

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"time"

	"academic/backend/internal/contract"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"academic/backend/internal/schedule"
	"github.com/go-chi/chi/v5"
)

// Routes must be mounted beneath session/CSRF middleware.
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{source}/login", func(w http.ResponseWriter, r *http.Request) {
		u, ok := httpkit.CurrentUser(r.Context())
		if !ok {
			httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
			return
		}
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if httpkit.DecodeJSON(w, r, &in) != nil {
			return
		}
		defer func() { in.Password = "" }()
		c, e := s.LoginPassword(r.Context(), u.ID, chi.URLParam(r, "source"), in.Username, in.Password)
		if e != nil {
			failHTTP(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 201, c)
	})
	r.Get("/sources", func(w http.ResponseWriter, r *http.Request) {
		httpkit.WriteJSON(w, 200, map[string]any{"items": s.Sources()})
	})
	r.Get("/connections", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		out, e := s.repo.List(r.Context(), u.ID)
		if e != nil {
			failHTTP(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": out})
	})
	r.Post("/{source}/auth", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		c, a, e := s.Begin(r.Context(), u.ID, chi.URLParam(r, "source"))
		if e != nil {
			failHTTP(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 201, map[string]any{"connection": c, "auth": authDTO(a)})
	})
	r.Route("/connections/{connection}", func(r chi.Router) {
		r.Post("/refresh-session", func(w http.ResponseWriter, r *http.Request) {
			connection, err := s.RefreshSession(r.Context(), requestScope(r))
			if err != nil {
				failHTTP(w, r, err)
				return
			}
			httpkit.WriteJSON(w, 200, map[string]any{"connection": connection, "session_refreshed": true})
		})
		r.Get("/auth", func(w http.ResponseWriter, r *http.Request) {
			a, e := s.Auth(r.Context(), requestScope(r))
			if e != nil {
				failHTTP(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 200, authDTO(a))
		})
		r.Get("/qr", func(w http.ResponseWriter, r *http.Request) {
			scope := requestScope(r)
			c, e := s.repo.Get(r.Context(), scope)
			if e != nil {
				failHTTP(w, r, e)
				return
			}
			s.qrMu.Lock()
			a := s.qr[scope]
			s.qrMu.Unlock()
			if !c.Enabled || (c.State != "pending" && c.State != "scanned") || a.Status == "authorized" || a.Status == "expired" || len(a.QRImage) == 0 || !time.Now().Before(a.ExpiresAt) {
				httpkit.Error(w, r, 404, "not_found", "二维码已失效，请重新登录")
				return
			}
			if a.QRMediaType != "image/png" && a.QRMediaType != "image/jpeg" {
				httpkit.Error(w, r, 502, "parse_failed", "二维码格式不可用")
				return
			}
			w.Header().Set("Content-Type", a.QRMediaType)
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(a.QRImage)
		})
		r.Delete("/", func(w http.ResponseWriter, r *http.Request) {
			if e := s.Disconnect(r.Context(), requestScope(r)); e != nil {
				failHTTP(w, r, e)
				return
			}
			w.WriteHeader(204)
		})
		r.Get("/discussion/{topic}/reply-users", func(w http.ResponseWriter, r *http.Request) {
			out, e := s.ReplyUsers(r.Context(), requestScope(r), r.URL.Query().Get("course_id"), r.URL.Query().Get("class_id"), chi.URLParam(r, "topic"))
			if e != nil {
				failHTTP(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 200, out)
		})
		r.Post("/discussion/{topic}/reply-users", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				CourseID string `json:"course_id"`
				ClassID  string `json:"class_id"`
			}
			if httpkit.DecodeJSON(w, r, &in) != nil {
				return
			}
			out, e := s.FetchReplyUsers(r.Context(), requestScope(r), in.CourseID, in.ClassID, chi.URLParam(r, "topic"))
			if e != nil {
				failHTTP(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 200, out)
		})
		r.Post("/sync", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Resource     contract.Resource  `json:"resource"`
				CourseID     string             `json:"course_id,omitempty"`
				ClassID      string             `json:"class_id,omitempty"`
				AcademicYear int                `json:"academic_year,omitempty"`
				Term         int                `json:"term,omitempty"`
				Calendar     *schedule.Calendar `json:"calendar,omitempty"`
			}
			if httpkit.DecodeJSON(w, r, &in) != nil {
				return
			}
			if in.Resource == contract.Schedule {
				if in.Calendar == nil || in.CourseID != "" || in.ClassID != "" {
					failHTTP(w, r, contract.ErrInvalid)
					return
				}
				result, e := s.SyncTimetable(r.Context(), requestScope(r), model.Semester{AcademicYear: in.AcademicYear, Term: in.Term}, *in.Calendar)
				if e != nil {
					failHTTP(w, r, e)
					return
				}
				httpkit.WriteJSON(w, 200, result)
				return
			}
			if in.Calendar != nil {
				failHTTP(w, r, contract.ErrInvalid)
				return
			}
			var syncErr error
			if in.AcademicYear != 0 || in.Term != 0 {
				if in.CourseID != "" || in.ClassID != "" {
					failHTTP(w, r, contract.ErrInvalid)
					return
				}
				syncErr = s.SyncTerm(r.Context(), requestScope(r), in.Resource, in.AcademicYear, in.Term)
			} else if in.CourseID != "" || in.ClassID != "" {
				if in.Resource != contract.Discussion {
					failHTTP(w, r, contract.ErrInvalid)
					return
				}
				syncErr = s.SyncDiscussionCourse(r.Context(), requestScope(r), in.CourseID, in.ClassID)
			} else {
				syncErr = s.Sync(r.Context(), requestScope(r), in.Resource)
			}
			if e := syncErr; e != nil {
				failHTTP(w, r, e)
				return
			}
			httpkit.WriteJSON(w, 200, map[string]string{"status": "page_committed"})
		})
		r.Get("/records", func(w http.ResponseWriter, r *http.Request) {
			limit := 20
			after := int64(0)
			var e error
			if v := r.URL.Query().Get("limit"); v != "" {
				limit, e = strconv.Atoi(v)
				if e != nil || limit < 1 || limit > 100 {
					failHTTP(w, r, model.ErrInvalidArgument)
					return
				}
			}
			if v := r.URL.Query().Get("after"); v != "" {
				after, e = strconv.ParseInt(v, 10, 64)
				if e != nil || after < 0 {
					failHTTP(w, r, model.ErrInvalidArgument)
					return
				}
			}
			items, e := s.repo.Records(r.Context(), requestScope(r), r.URL.Query().Get("resource"), after, limit+1)
			if e != nil {
				failHTTP(w, r, e)
				return
			}
			next := int64(0)
			if len(items) > limit {
				items = items[:limit]
				next = items[len(items)-1].ID
			}
			httpkit.WriteJSON(w, 200, map[string]any{"items": items, "next_after": next})
		})
	})
	return r
}
func requestScope(r *http.Request) model.DWSScope {
	u, _ := httpkit.CurrentUser(r.Context())
	return model.DWSScope{UserID: u.ID, ConnectionID: chi.URLParam(r, "connection")}
}
func authDTO(a contract.AuthResult) map[string]any {
	delay := a.PollAfter
	if delay < 2*time.Second {
		delay = 2 * time.Second
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	out := map[string]any{"status": a.Status, "expires_at": a.ExpiresAt, "poll_after_ms": delay.Milliseconds()}
	if a.Status == "pending" || a.Status == "scanned" {
		if a.QRURL != "" {
			out["qr_url"] = a.QRURL
		}
		if len(a.QRImage) > 0 && len(a.QRImage) <= 512<<10 && (a.QRMediaType == "image/png" || a.QRMediaType == "image/jpeg") {
			out["qr_image"] = "data:" + a.QRMediaType + ";base64," + base64.StdEncoding.EncodeToString(a.QRImage)
		}
	}
	return out
}
func failHTTP(w http.ResponseWriter, r *http.Request, e error) {
	code, status := errorCode(e), 502
	switch {
	case errors.Is(e, model.ErrNotFound):
		code, status = "not_found", 404
	case errors.Is(e, model.ErrConflict), errors.Is(e, model.ErrLeaseLost):
		code, status = "conflict", 409
	case errors.Is(e, model.ErrInvalidArgument), errors.Is(e, contract.ErrInvalid):
		code, status = "invalid_argument", 400
	case errors.Is(e, contract.ErrUnsupported):
		status = 422
	case errors.Is(e, contract.ErrAuthRequired):
		status = 409
	case errors.Is(e, contract.ErrRateLimited), errors.Is(e, contract.ErrVerificationRequired):
		status = 429
	}
	httpkit.Error(w, r, status, code, "平台操作未完成："+code)
}
