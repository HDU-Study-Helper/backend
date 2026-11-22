package runcontrol

import (
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := httpkit.CurrentUser(r.Context())
			if !ok {
				httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
				return
			}
			if u.Role != "admin" {
				httpkit.Error(w, r, 403, "permission_denied", "仅管理员可查看运行控制")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	respond := func(w http.ResponseWriter, r *http.Request, v any, e error) {
		if e != nil {
			status := 500
			code, msg := "internal_error", "读取或更新失败"
			switch {
			case errors.Is(e, model.ErrNotFound):
				status, code, msg = 404, "not_found", "记录不存在"
			case errors.Is(e, model.ErrConflict):
				status, code, msg = 409, "conflict", "状态已变化或需要核查子任务，请刷新后重试"
			case errors.Is(e, model.ErrInvalidArgument):
				status, code, msg = 400, "invalid_argument", "请指定刷新轮次或批次"
			}
			httpkit.Error(w, r, status, code, msg)
			return
		}
		httpkit.WriteJSON(w, 200, v)
	}
	if first, ok := s.Store.(model.FirstRunRepository); ok {
		r.Get("/first-run", func(w http.ResponseWriter, r *http.Request) {
			u, _ := httpkit.CurrentUser(r.Context())
			v, e := first.FirstStatus(r.Context(), u.ID)
			respond(w, r, v, e)
		})
		r.Post("/first-run", func(w http.ResponseWriter, r *http.Request) {
			u, _ := httpkit.CurrentUser(r.Context())
			v, e := first.StartFirst(r.Context(), u.ID)
			if errors.Is(e, model.ErrNotFound) {
				httpkit.Error(w, r, 409, "no_selected_courses", "请先导入本学期教务选课列表，再启动首轮")
				return
			}
			if errors.Is(e, model.ErrConflict) {
				httpkit.Error(w, r, 409, "not_ready", "请先确保有且只有一个已启用的 Learner，再启动首轮")
				return
			}
			respond(w, r, v, e)
		})
	}
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Overview(r.Context())
		respond(w, r, v, e)
	})
	r.Get("/batches/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.GetBatch(r.Context(), chi.URLParam(r, "id"))
		if e != nil {
			respond(w, r, nil, e)
			return
		}
		runs, e := s.Store.BatchRuns(r.Context(), v.ID)
		result := map[string]any{"batch": v, "runs": runs}
		if store, ok := s.Store.(model.ControlItemRepository); ok && e == nil {
			result["items"], e = store.ListItems(r.Context(), model.ControlListQuery{BatchID: v.ID, Limit: 500})
			if e == nil {
				result["artifacts"], e = store.ListArtifacts(r.Context(), model.ControlListQuery{BatchID: v.ID, Limit: 500})
			}
		}
		respond(w, r, result, e)
	})
	query := func(r *http.Request) model.ControlListQuery {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		status := q.Get("status")
		if status == "" {
			status = q.Get("state")
		}
		return model.ControlListQuery{RoundID: q.Get("round_id"), BatchID: q.Get("batch_id"), ItemID: q.Get("item_id"), Status: status, Type: q.Get("type"), After: q.Get("after"), Limit: limit, Offset: offset}
	}
	r.Get("/items", func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.Store.(model.ControlItemRepository)
		if !ok {
			respond(w, r, nil, model.ErrNotFound)
			return
		}
		q := query(r)
		items, e := store.ListItems(r.Context(), q)
		counts, ce := store.ItemCounts(r.Context(), q.RoundID)
		if e == nil {
			e = ce
		}
		next := ""
		if len(items) > 0 {
			next = items[len(items)-1].ID
		}
		respond(w, r, map[string]any{"items": items, "counts": counts, "next_after": next}, e)
	})
	r.Get("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.Store.(model.ControlItemRepository)
		if !ok {
			respond(w, r, nil, model.ErrNotFound)
			return
		}
		item, e := store.GetItem(r.Context(), chi.URLParam(r, "id"))
		if e != nil {
			respond(w, r, nil, e)
			return
		}
		runs, e := s.Store.BatchRuns(r.Context(), item.BatchID)
		filtered := []model.ControlRun{}
		for _, run := range runs {
			if run.ItemID == item.ID || run.ItemID == item.ParentID {
				filtered = append(filtered, run)
			}
		}
		artifacts, ae := store.ListArtifacts(r.Context(), model.ControlListQuery{ItemID: item.ID, Limit: 500})
		if e == nil {
			e = ae
		}
		if item.ParentID != "" && e == nil {
			parentArtifacts, err := store.ListArtifacts(r.Context(), model.ControlListQuery{ItemID: item.ParentID, Limit: 500})
			if err != nil {
				e = err
			} else {
				allowedRuns := map[string]bool{}
				var routes []itemRoute
				_ = json.Unmarshal(item.Routes, &routes)
				for _, route := range routes {
					if route.Execution != nil {
						allowedRuns[route.Execution.RunID] = true
					}
				}
				for _, artifact := range parentArtifacts {
					if allowedRuns[artifact.RunID] {
						artifacts = append(artifacts, artifact)
					}
				}
			}
		}
		respond(w, r, map[string]any{"item": item, "runs": filtered, "artifacts": artifacts}, e)
	})
	r.Get("/artifacts", func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.Store.(model.ControlItemRepository)
		if !ok {
			respond(w, r, nil, model.ErrNotFound)
			return
		}
		items, e := store.ListArtifacts(r.Context(), query(r))
		next := ""
		if len(items) > 0 {
			next = items[len(items)-1].ID
		}
		respond(w, r, map[string]any{"items": items, "next_after": next}, e)
	})
	r.Get("/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.GetRun(r.Context(), chi.URLParam(r, "id"))
		respond(w, r, v, e)
	})
	r.Post("/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Round  string `json:"round_id"`
			Source string `json:"source"`
			Batch  string `json:"batch_id"`
		}
		if httpkit.DecodeJSON(w, r, &in) != nil {
			return
		}
		n, e := s.Store.Activate(r.Context(), in.Round, in.Source, in.Batch, false)
		respond(w, r, map[string]any{"accepted": n}, e)
	})
	r.Post("/batches/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		n, e := s.Store.Activate(r.Context(), "", "", chi.URLParam(r, "id"), true)
		if e == nil && n == 0 {
			e = model.ErrConflict
		}
		respond(w, r, map[string]any{"accepted": n}, e)
	})
	r.Post("/batches/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		e := s.Cancel(r.Context(), chi.URLParam(r, "id"))
		respond(w, r, map[string]any{"accepted": e == nil}, e)
	})
	return r
}
