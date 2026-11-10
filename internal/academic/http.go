package academic

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5"
)

type Service struct{ repo model.AcademicRepository }

func New(repo model.AcademicRepository) *Service { return &Service{repo: repo} }

// Routes requires users.Middleware at its mount and independently checks Actor.
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := httpkit.CurrentUser(r.Context()); !ok {
				httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/offerings", s.offerings)
	r.Get("/items", s.items)
	r.Get("/tree", s.tree)
	r.Get("/collection-policy", s.collectionPolicy)
	r.Put("/collection-policy", s.saveCollectionPolicy)
	return r
}

var exactTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-](?:[01]\d|2[0-3]):[0-5]\d)$`)

func parseQuery(values url.Values, mode string) (model.AcademicQuery, error) {
	q := model.AcademicQuery{Limit: 20}
	allowed := map[string]int{"source": 32, "connection_id": 128}
	if mode != "tree" {
		allowed["course_ref"] = 4096
		allowed["offset"] = 12
		allowed["limit"] = 3
	}
	if mode == "items" {
		for _, k := range []string{"offering_id", "kind", "status", "search", "published_after", "published_before", "deadline_after", "deadline_before"} {
			allowed[k] = 4096
		}
	}
	if mode == "tree" {
		allowed["format"] = 8
	}
	for key, v := range values {
		max, ok := allowed[key]
		if !ok || len(v) != 1 || len(v[0]) > max || strings.ContainsAny(v[0], "\x00\r\n") {
			return q, model.ErrInvalidArgument
		}
	}
	q.Source = values.Get("source")
	q.ConnectionID = values.Get("connection_id")
	q.CourseRef = values.Get("course_ref")
	q.OfferingID = values.Get("offering_id")
	q.Kind = values.Get("kind")
	q.Status = values.Get("status")
	q.Search = values.Get("search")
	if len(q.OfferingID) > 128 || len(q.Kind) > 64 || len(q.Status) > 64 || len(q.Search) > 1000 {
		return q, model.ErrInvalidArgument
	}
	if mode != "tree" {
		for _, field := range []struct {
			name string
			dst  *int
		}{{"limit", &q.Limit}, {"offset", &q.Offset}} {
			if values.Has(field.name) {
				n, e := strconv.Atoi(values.Get(field.name))
				if e != nil || n < 0 || (field.name == "limit" && (n < 1 || n > 100)) {
					return q, model.ErrInvalidArgument
				}
				*field.dst = n
			}
		}
	}
	for _, field := range []struct {
		name string
		dst  **time.Time
	}{{"published_after", &q.PublishedAfter}, {"published_before", &q.PublishedBefore}, {"deadline_after", &q.DeadlineAfter}, {"deadline_before", &q.DeadlineBefore}} {
		if values.Has(field.name) {
			raw := values.Get(field.name)
			if !exactTime.MatchString(raw) {
				return q, model.ErrInvalidArgument
			}
			t, e := time.Parse(time.RFC3339, raw)
			if e != nil {
				return q, model.ErrInvalidArgument
			}
			*field.dst = &t
		}
	}
	if (q.PublishedAfter != nil && q.PublishedBefore != nil && q.PublishedAfter.After(*q.PublishedBefore)) || (q.DeadlineAfter != nil && q.DeadlineBefore != nil && q.DeadlineAfter.After(*q.DeadlineBefore)) {
		return q, model.ErrInvalidArgument
	}
	if mode == "tree" && values.Has("format") && values.Get("format") != "json" && values.Get("format") != "text" {
		return q, model.ErrInvalidArgument
	}
	return q, nil
}
func (s *Service) offerings(w http.ResponseWriter, r *http.Request) {
	q, e := parseQuery(r.URL.Query(), "offerings")
	if e != nil {
		fail(w, r, e)
		return
	}
	u, _ := httpkit.CurrentUser(r.Context())
	items, e := s.repo.Offerings(r.Context(), u.ID, q)
	if e != nil {
		fail(w, r, e)
		return
	}
	if items == nil {
		items = []model.AcademicOffering{}
	}
	httpkit.WriteJSON(w, 200, map[string]any{"items": items, "offset": q.Offset, "limit": q.Limit})
}
func (s *Service) items(w http.ResponseWriter, r *http.Request) {
	q, e := parseQuery(r.URL.Query(), "items")
	if e != nil {
		fail(w, r, e)
		return
	}
	u, _ := httpkit.CurrentUser(r.Context())
	items, e := s.repo.Items(r.Context(), u.ID, q)
	if e != nil {
		fail(w, r, e)
		return
	}
	if items == nil {
		items = []model.AcademicItem{}
	}
	httpkit.WriteJSON(w, 200, map[string]any{"items": items, "offset": q.Offset, "limit": q.Limit})
}
func (s *Service) tree(w http.ResponseWriter, r *http.Request) {
	q, e := parseQuery(r.URL.Query(), "tree")
	if e != nil {
		fail(w, r, e)
		return
	}
	u, _ := httpkit.CurrentUser(r.Context())
	tree, e := s.repo.Tree(r.Context(), u.ID, q.Source, q.ConnectionID)
	if e != nil {
		fail(w, r, e)
		return
	}
	if tree.Offerings == nil {
		tree.Offerings = []model.AcademicOffering{}
	}
	if tree.Items == nil {
		tree.Items = []model.AcademicItem{}
	}
	if r.URL.Query().Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(RenderTree(tree)))
		return
	}
	httpkit.WriteJSON(w, 200, struct {
		model.AcademicTree
		Directory []*Node `json:"directory"`
	}{tree, Directory(tree)})
}
func fail(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := 500, "internal", "无法读取学业归档；未返回部分目录，请缩小来源或连接范围后重试"
	switch {
	case errors.Is(e, model.ErrInvalidArgument):
		status, code, message = 400, "invalid_argument", "筛选条件无效；时间须为带时区的 RFC3339，limit 为 1–100，offset 非负，不能传 user_id"
	case errors.Is(e, model.ErrNotFound):
		status, code, message = 404, "not_found", "连接不存在或不属于当前用户"
	case errors.Is(e, model.ErrUnsupported):
		status, code, message = 422, "unsupported", "当前归档不支持此读取操作"
	case errors.Is(e, model.ErrConflict):
		status, code, message = 422, "tree_too_large", "归档目录超过完整快照容量，未返回部分数据；请指定来源或连接，或使用 offerings/items 分页查询"
	}
	httpkit.Error(w, r, status, code, message)
}
