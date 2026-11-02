package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"academic/backend/internal/bridge"
	"academic/backend/internal/dws"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/platform"
	"academic/backend/internal/preview"
	"academic/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func Router(userService *users.Service, dwsService *dws.Service, port bridge.Port, platforms ...*platform.Service) http.Handler {
	return routerWithPreview(userService, dwsService, port, nil, platforms...)
}
func routerWithPreview(userService *users.Service, dwsService *dws.Service, port bridge.Port, inspect *preview.Service, platforms ...*platform.Service) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	if userService == nil {
		return r
	}
	r.Get("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/admin", http.StatusSeeOther) })
	r.Mount("/admin", userService.Web())
	r.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(userService.Middleware)
			if inspect != nil {
				r.Mount("/preview", inspect.Routes())
			}
			if len(platforms) > 0 && platforms[0] != nil {
				r.Mount("/platforms", platforms[0].Routes())
			}
			r.Post("/bridge/demo", func(w http.ResponseWriter, r *http.Request) {
				if port == nil {
					httpkit.Error(w, r, 422, "unsupported", "通信端口尚未配置")
					return
				}
				actor, ok := httpkit.CurrentUser(r.Context())
				if !ok {
					httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
					return
				}
				var input struct {
					Payload json.RawMessage `json:"payload"`
				}
				if err := httpkit.DecodeJSON(w, r, &input); err != nil {
					return
				}
				reply, err := port.Call(r.Context(), bridge.Request{Version: bridge.Version, OwnerID: actor.ID, Operation: "demo.echo", Payload: input.Payload})
				if err != nil {
					if errors.Is(err, bridge.ErrInvalid) {
						httpkit.Error(w, r, 400, "invalid_argument", "Demo 内容必须是小于 4KiB 的有效 JSON")
					} else {
						httpkit.Error(w, r, 422, "unsupported", "不支持此通信操作")
					}
					return
				}
				httpkit.WriteJSON(w, 200, reply)
			})
			if dwsService != nil {
				r.Mount("/dws", dwsService.Routes())
			} else {
				unavailable := func(w http.ResponseWriter, r *http.Request) {
					httpkit.Error(w, r, 503, "unavailable", "DWS 尚未配置，请配置官方 CLI 和加密密钥")
				}
				r.HandleFunc("/dws", unavailable)
				r.HandleFunc("/dws/*", unavailable)
			}
		})
		r.Mount("/", userService.Routes())
	})
	return r
}
