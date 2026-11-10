package scheduler

import (
	"academic/backend/internal/httpkit"
	"embed"
	"github.com/go-chi/chi/v5"
	"net/http"
	"sort"
)

//go:embed tasks.html
var pages embed.FS

func Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	data, _ := pages.ReadFile("tasks.html")
	_, _ = w.Write(data)
}
func Routes(tasks ...*Task) http.Handler {
	catalog := map[string]*Task{}
	for _, task := range tasks {
		if task != nil {
			catalog[task.Snapshot().ID] = task
		}
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := httpkit.CurrentUser(r.Context())
			if !ok {
				httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
				return
			}
			if u.Role != "admin" {
				httpkit.Error(w, r, 403, "permission_denied", "仅管理员可管理全局计划任务")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		items := []State{}
		for _, task := range catalog {
			items = append(items, task.Snapshot())
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		httpkit.WriteJSON(w, 200, map[string]any{"items": items, "persistent": false})
	})
	taskFor := func(w http.ResponseWriter, r *http.Request) *Task {
		task := catalog[chi.URLParam(r, "task")]
		if task == nil {
			httpkit.Error(w, r, 404, "not_found", "任务不存在或服务尚未配置")
		}
		return task
	}
	r.Put("/{task}", func(w http.ResponseWriter, r *http.Request) {
		task := taskFor(w, r)
		if task == nil {
			return
		}
		var input struct {
			Enabled         *bool `json:"enabled"`
			IntervalSeconds int   `json:"interval_seconds"`
		}
		if httpkit.DecodeJSON(w, r, &input) != nil {
			return
		}
		if input.Enabled == nil || input.IntervalSeconds < 60 || input.IntervalSeconds > 86400 {
			httpkit.Error(w, r, 400, "invalid_argument", "请指定启用状态与60至86400秒的运行间隔")
			return
		}
		task.Update(*input.Enabled, input.IntervalSeconds)
		httpkit.WriteJSON(w, 200, task.Snapshot())
	})
	r.Post("/{task}/trigger", func(w http.ResponseWriter, r *http.Request) {
		task := taskFor(w, r)
		if task == nil {
			return
		}
		if !task.Trigger() {
			httpkit.Error(w, r, 409, "task_busy", "任务尚未启动、已排队或正在运行，请刷新状态")
			return
		}
		httpkit.WriteJSON(w, 202, map[string]any{"accepted": true, "task": task.Snapshot()})
	})
	return r
}
