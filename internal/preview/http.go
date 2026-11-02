// Package preview exposes read-only, owner-scoped archive inspection.
package preview

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"academic/backend/internal/contract"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5"
)

type Service struct {
	repo        model.PreviewRepository
	descriptors map[string]contract.Descriptor
	dwsInterval int
}

func New(repo model.PreviewRepository, descriptors []contract.Descriptor, dwsInterval int) *Service {
	s := &Service{repo: repo, descriptors: map[string]contract.Descriptor{}, dwsInterval: dwsInterval}
	for _, d := range descriptors {
		s.descriptors[d.Source] = d
	}
	return s
}
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := httpkit.CurrentUser(r.Context())
			if !ok || u.ID == "" {
				httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/overview", func(w http.ResponseWriter, r *http.Request) {
		u, _ := httpkit.CurrentUser(r.Context())
		items, e := s.repo.Overview(r.Context(), u.ID)
		if e != nil {
			fail(w, r, e)
			return
		}
		for i := range items {
			c := &items[i]
			if c.Resources == nil {
				c.Resources = []model.ResourcePreview{}
			}
			d := s.descriptors[c.Source]
			for _, resource := range d.Resources {
				found := false
				for _, v := range c.Resources {
					if v.Kind == string(resource) {
						found = true
					}
				}
				if !found {
					c.Resources = append(c.Resources, model.ResourcePreview{Kind: string(resource)})
				}
			}
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": items, "poll_interval_seconds": 60, "dws_poll_interval_seconds": s.dwsInterval, "notes": []string{"只预览当前登录用户的归档，不发起上游抓取。", "每个原生对象仅保留最新状态；记录数与逻辑对象数按相同身份口径统计。", "学习平台每轮结束后等待60秒，每资源读取一页；连接多时实际间隔更长。", "最近成功表示一页已提交，不代表所有分页完成；空页成功也会更新成功时间。", "历史抓取在启用状态统计前可能没有尝试时间或耗时；最近成功可由checkpoint恢复。", "DWS按已有任务和消息归档显示；附件二进制不进入本窗口。"}})
	})
	r.Get("/courses", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		u, _ := httpkit.CurrentUser(r.Context())
		if len(q.Get("connection_id")) > 128 || q.Get("source") != "chaoxing" {
			fail(w, r, model.ErrInvalidArgument)
			return
		}
		repo, ok := s.repo.(model.CoursePreviewRepository)
		if !ok {
			fail(w, r, model.ErrInternal)
			return
		}
		items, unassociated, e := repo.Courses(r.Context(), u.ID, q.Get("source"), q.Get("connection_id"))
		if e != nil {
			fail(w, r, e)
			return
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": items, "unassociated_count": unassociated})
	})
	r.Get("/records", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 20
		after := int64(0)
		var e error
		if raw := q.Get("limit"); raw != "" {
			limit, e = strconv.Atoi(raw)
			if e != nil || limit < 1 || limit > 50 {
				fail(w, r, model.ErrInvalidArgument)
				return
			}
		}
		if raw := q.Get("after"); raw != "" {
			after, e = strconv.ParseInt(raw, 10, 64)
			if e != nil || after < 0 {
				fail(w, r, model.ErrInvalidArgument)
				return
			}
		}
		if len(q.Get("connection_id")) > 128 || len(q.Get("source")) > 32 || len(q.Get("resource")) > 32 {
			fail(w, r, model.ErrInvalidArgument)
			return
		}
		u, _ := httpkit.CurrentUser(r.Context())
		var items []model.PlatformRecord
		if key := q.Get("course_key"); key != "" {
			if len(key) > 4096 {
				fail(w, r, model.ErrInvalidArgument)
				return
			}
			repo, ok := s.repo.(model.CoursePreviewRepository)
			if !ok {
				fail(w, r, model.ErrInternal)
				return
			}
			items, e = repo.CourseRecords(r.Context(), u.ID, q.Get("source"), q.Get("connection_id"), q.Get("resource"), key, after, limit+1)
		} else {
			items, e = s.repo.Records(r.Context(), u.ID, q.Get("source"), q.Get("connection_id"), q.Get("resource"), after, limit+1)
		}
		if e != nil {
			fail(w, r, e)
			return
		}
		if items == nil {
			items = []model.PlatformRecord{}
		}
		next := int64(0)
		if len(items) > limit {
			items = items[:limit]
			next = items[len(items)-1].ID
		}
		// Bound each response even if archived source texts are unusually large.
		total := 0
		for i, item := range items {
			total += len(item.Content)
			if total > 4<<20 {
				if i == 0 {
					fail(w, r, model.ErrInternal)
					return
				}
				items = items[:i]
				next = items[i-1].ID
				break
			}
		}
		for _, item := range items {
			if !json.Valid(item.Content) {
				fail(w, r, model.ErrInternal)
				return
			}
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": items, "next_after": next})
	})
	return r
}
func fail(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := 500, "internal", "无法读取预览数据"
	if errors.Is(e, model.ErrNotFound) {
		status, code, message = 404, "not_found", "连接不存在或不属于当前用户"
	} else if errors.Is(e, model.ErrInvalidArgument) {
		status, code, message = 400, "invalid_argument", "预览筛选条件无效"
	}
	httpkit.Error(w, r, status, code, message)
}
