package academic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
)

type fakeRepo struct {
	owner, source, connection string
	q                         model.AcademicQuery
	calls                     int
	err                       error
	snapshot                  model.AcademicTree
}

func (f *fakeRepo) Offerings(_ context.Context, owner string, q model.AcademicQuery) ([]model.AcademicOffering, error) {
	f.owner = owner
	f.q = q
	f.calls++
	return nil, f.err
}
func (f *fakeRepo) Items(_ context.Context, owner string, q model.AcademicQuery) ([]model.AcademicItem, error) {
	f.owner = owner
	f.q = q
	f.calls++
	return nil, f.err
}
func (f *fakeRepo) Tree(_ context.Context, owner, source, connection string) (model.AcademicTree, error) {
	f.owner = owner
	f.source = source
	f.connection = connection
	f.calls++
	return f.snapshot, f.err
}
func (f *fakeRepo) Rebuild(context.Context, string, string) (model.AcademicRebuildResult, error) {
	panic("read-only service must never rebuild")
}
func request(h http.Handler, path, owner string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if owner != "" {
		r = r.WithContext(httpkit.WithUser(r.Context(), model.User{ID: owner}))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestAuthenticationAndOwner(t *testing.T) {
	for _, path := range []string{"/offerings", "/items", "/tree", "/tree?format=text"} {
		t.Run(path, func(t *testing.T) {
			f := &fakeRepo{}
			h := New(f).Routes()
			w := request(h, path, "")
			if w.Code != 401 || f.calls != 0 {
				t.Fatalf("unauthenticated status=%d calls=%d", w.Code, f.calls)
			}
			w = request(h, path, "alice")
			if w.Code != 200 || f.owner != "alice" {
				t.Fatalf("owner status=%d owner=%q", w.Code, f.owner)
			}
			if strings.Contains(w.Body.String(), "bob-private") {
				t.Fatal("cross-owner output")
			}
		})
	}
}
func TestValidationBeforeRepository(t *testing.T) {
	cases := []string{
		"/items?user_id=bob", "/tree?user_id=bob", "/offerings?user_id=bob", "/items?limit=101", "/items?limit=0", "/items?limit=-1", "/items?offset=-1", "/items?limit=x", "/items?limit=2&limit=3", "/items?published_after=2026-01-01", "/items?deadline_before=2026-01-01T00:00:00", "/items?deadline_before=2026-01-01T00:00:00%2B24:00", "/items?published_after=2026-01-01T1:00:00Z", "/items?published_after=2026-01-02T00:00:00Z&published_before=2026-01-01T00:00:00Z", "/items?deadline_after=2026-01-02T00:00:00Z&deadline_before=2026-01-01T00:00:00Z", "/tree?format=html", "/tree?limit=100", "/offerings?search=x", "/items?search=" + strings.Repeat("x", 1001), "/items?connection_id=" + strings.Repeat("x", 129), "/items?search=%00",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			f := &fakeRepo{}
			w := request(New(f).Routes(), path, "alice")
			if w.Code != 400 || f.calls != 0 {
				t.Fatalf("status %d calls %d body %s", w.Code, f.calls, w.Body.String())
			}
		})
	}
}
func TestFilterPropagation(t *testing.T) {
	v := url.Values{"source": {"mooc"}, "connection_id": {"conn"}, "offering_id": {"off"}, "course_ref": {"compound"}, "kind": {"assignment"}, "status": {"submitted"}, "search": {"实验报告"}, "published_after": {"2026-01-01T00:00:00+08:00"}, "published_before": {"2026-02-01T00:00:00Z"}, "deadline_after": {"2026-03-01T00:00:00Z"}, "deadline_before": {"2026-04-01T00:00:00Z"}, "limit": {"100"}, "offset": {"200"}}
	f := &fakeRepo{}
	w := request(New(f).Routes(), "/items?"+v.Encode(), "alice")
	q := f.q
	if w.Code != 200 || q.Source != "mooc" || q.ConnectionID != "conn" || q.OfferingID != "off" || q.CourseRef != "compound" || q.Kind != "assignment" || q.Status != "submitted" || q.Search != "实验报告" || q.Limit != 100 || q.Offset != 200 || q.PublishedAfter == nil || q.PublishedBefore == nil || q.DeadlineAfter == nil || q.DeadlineBefore == nil {
		t.Fatalf("status=%d query=%+v", w.Code, q)
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatal("list must be []")
	}
}
func TestErrorsNeverReturnPartialData(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{model.ErrNotFound, 404}, {model.ErrUnsupported, 422}, {fmt.Errorf("%w: capacity", model.ErrConflict), 422}, {errors.New("bob-private SQL credential"), 500}} {
		f := &fakeRepo{err: tc.err, snapshot: model.AcademicTree{Items: []model.AcademicItem{{Title: "bob-private"}}}}
		w := request(New(f).Routes(), "/tree?format=text", "alice")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "bob-private") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
}
func TestTreeFormatsAndPageEscaping(t *testing.T) {
	f := &fakeRepo{snapshot: model.AcademicTree{Items: []model.AcademicItem{{Title: `<script>alert('x')</script>`, Text: "正文", Source: "mooc", ConnectionID: "c", Kind: "notice"}}}}
	s := New(f)
	w := request(s.Routes(), "/tree?source=mooc&connection_id=c", "alice")
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("JSON unsafe or failed")
	}
	var data struct {
		Directory []*Node `json:"directory"`
	}
	if json.Unmarshal(w.Body.Bytes(), &data) != nil || len(data.Directory) != 1 {
		t.Fatal("directory missing")
	}
	w = request(s.Routes(), "/tree?format=text", "alice")
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Body.String(), "正文") {
		t.Fatal("text export missing")
	}
	p := httptest.NewRecorder()
	s.Page(p, httptest.NewRequest("GET", "/admin/academic", nil))
	if strings.Contains(p.Body.String(), "alert('x')") || strings.Contains(p.Body.String(), "innerHTML") || !strings.Contains(p.Body.String(), "textContent") || !strings.Contains(p.Body.String(), "/admin") {
		t.Fatal("page must use text-only dynamic rendering and provide login")
	}
}
func TestOfferingDescriptionAndDetails(t *testing.T) {
	tree := model.AcademicTree{Offerings: []model.AcademicOffering{{ID: "offering", Source: "mooc", ConnectionID: "c", Name: "数学", Description: "介绍极限与微积分\n包括应用", Details: json.RawMessage(`{"requirements":"完成练习","teachingGoal":"理解微积分","target":"应用分析","courseLoad":48,"courseStyle":"线上","courseName":"高等数学","teachers":[{"name":"张老师"}]}`)}}}
	text := RenderTree(tree)
	for _, want := range []string{"课程简介：介绍极限与微积分", "包括应用", "学习要求=完成练习", "教学目标=理解微积分", "课程目标=应用分析", "课程学时 / 学习负担=48", "授课方式=线上", "课程名称=高等数学", "教师团队=名称=张老师"} {
		if !strings.Contains(text, want) {
			t.Errorf("offering detail missing %q", want)
		}
	}
	w := request(New(&fakeRepo{snapshot: tree}).Routes(), "/tree?format=text", "alice")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "完成练习") {
		t.Fatal(w.Body.String())
	}
}

func TestReadableTimeAndCoverage(t *testing.T) {
	value := time.Date(2026, 11, 10, 1, 2, 3, 0, time.UTC)
	tm := model.AcademicTime{Value: &value, Raw: "1794272523000", State: "known", Precision: "instant"}
	if got := timeText(tm); !strings.Contains(got, "2026-11-10 09:02:03") || strings.Contains(got, tm.Raw) {
		t.Fatalf("unreadable instant %s", got)
	}
	if got := timeText(model.AcademicTime{Date: "2026-11-10", State: "known", Precision: "day"}); !strings.HasPrefix(got, "2026-11-10 ") || strings.Contains(got, "00:00") {
		t.Fatalf("day gained a clock %s", got)
	}
	tree := model.AcademicTree{Items: []model.AcademicItem{{Source: "mooc", ConnectionID: "a", Kind: "course_observation", Published: tm}}}
	compact := RenderCompactTree(tree)
	if !strings.Contains(compact, "最后访问的单元") || !strings.Contains(compact, "2026-11-10 09:02:03") || strings.Contains(compact, tm.Raw) {
		t.Fatal(compact)
	}
	coverage := json.RawMessage(`{"resources":{"exam":{"state":"observed","record_count":13,"item_count":1,"evidence_record_count":13}}}`)
	if got := coverageSummary(coverage); !strings.Contains(got, "考试 1 项") || strings.Contains(got, "考试 13") {
		t.Fatal(got)
	}
	if got := coverageSummary(json.RawMessage(`{"resources":{"exam":{"state":"observed","record_count":13}}}`)); !strings.Contains(got, "原始证据 13 条") {
		t.Fatal(got)
	}
}

func TestDirectoryEveryItemAndScope(t *testing.T) {
	tree := model.AcademicTree{Offerings: []model.AcademicOffering{{ID: "same", Source: "mooc", ConnectionID: "a", Name: "高等数学", NativeTermID: "term-a"}, {ID: "same", Source: "mooc", ConnectionID: "b", Name: "高等数学", NativeTermID: "term-b"}}}
	for i := 0; i < 150; i++ {
		tree.Items = append(tree.Items, model.AcademicItem{ID: fmt.Sprint(i), Source: "mooc", ConnectionID: "a", OfferingID: "same", Kind: "material", Title: fmt.Sprintf("资源%03d", i), Path: []string{"第一章", "第一课时"}})
	}
	tree.Items = append(tree.Items, model.AcademicItem{Source: "mooc", ConnectionID: "b", Kind: "notice", Title: "独立通知"})
	seen := map[int]int{}
	var walk func([]*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Item != nil {
				seen[*n.Item]++
			}
			walk(n.Children)
		}
	}
	walk(Directory(tree))
	if len(seen) != len(tree.Items) {
		t.Fatalf("lost items %d of %d", len(seen), len(tree.Items))
	}
	for _, n := range seen {
		if n != 1 {
			t.Fatal("duplicate item")
		}
	}
	text := RenderTree(tree)
	compact := RenderCompactTree(tree)
	if strings.Contains(compact, "无值") || len(strings.Split(compact, "\n")) > 180 {
		t.Fatal("compact directory contains repeated details")
	}
	for _, s := range []string{"term-a", "term-b", "第一章", "第一课时", "资源149", "150 条", "未关联课程", "独立通知", "完整性未知"} {
		if !strings.Contains(compact, s) {
			t.Errorf("compact missing %q", s)
		}
		if !strings.Contains(text, s) {
			t.Errorf("missing %q", s)
		}
	}
}
