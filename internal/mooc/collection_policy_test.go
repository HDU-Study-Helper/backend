package mooc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func policyCursor(t *testing.T, c cursor) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return base64Cursor(b)
}

func TestExcludedCourseBeforeEveryCourseRead(t *testing.T) {
	for _, kind := range []contract.Resource{contract.Course, contract.Assignment, contract.Exam, contract.Progress, contract.Notice, "chapter", "lesson", "material", "discussion"} {
		for _, phase := range []string{"discovery", "route", "active", "info"} {
			t.Run(string(kind)+"/"+phase, func(t *testing.T) {
				calls := 0
				a := testAdapter(func(*http.Request) (*http.Response, error) {
					calls++
					t.Fatal("excluded course made a course-specific request")
					return nil, nil
				})
				s := sessionFor(t, a, scope, "fixture")
				co := course{ID: "10", Term: "101", Type: 2, Name: "Same name", Discovered: phase != "discovery"}
				if phase == "active" {
					co.SourceTerm, co.ActiveTerm, co.ActiveRead = "202", "202", true
				}
				if phase == "info" {
					co.InfoPending = true
					co.Metadata = map[string]any{"sourceCourseId": "20"}
				}
				c, _ := newCursor(scope, kind, "")
				c.Courses, c.Done = []course{co}, true
				ctx := contract.WithExcludedCourses(context.Background(), scope, []string{co.ref()})
				p, err := a.Sync(ctx, scope, s, kind, policyCursor(t, c))
				if err != nil || p.HasMore || len(p.Records) != 0 || calls != 0 {
					t.Fatalf("blocked page: %+v err=%v calls=%d", p, err, calls)
				}
			})
		}
	}
}

func TestExclusionAddedAfterDiscoveryKeepsSavedIndices(t *testing.T) {
	shared, specific := 0, 0
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "getMyLearnedCoursePanelList") {
			shared++
			return response(`{"code":0,"result":{"result":[{"id":10,"name":"Same name","termPanel":{"id":101},"schoolPanel":{"shortName":"TEST"}},{"id":10,"name":"Same name","termPanel":{"id":102}}],"pagination":{"totalPageCount":1}}}`), nil
		}
		specific++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("termId") != "102" {
			t.Fatal("requested excluded term", r.Form)
		}
		return response(`{"code":0,"result":{"mocTermDto":{"id":102,"chapters":[{"id":1,"name":"Allowed chapter"}]}}}`), nil
	})
	s := sessionFor(t, a, scope, "fixture")
	p, err := a.Sync(context.Background(), scope, s, "chapter", "")
	if err != nil || !p.HasMore || shared != 1 || specific != 0 {
		t.Fatal(p, err, shared, specific)
	}
	ctx := contract.WithExcludedCourses(context.Background(), scope, []string{key("10", "101")})
	old := p.NextCursor
	p, err = a.Sync(ctx, scope, p.Session, "chapter", old)
	if err != nil || !p.HasMore || p.NextCursor == old || len(p.Records) != 0 || specific != 0 {
		t.Fatal("excluded cursor did not advance without network", p, err, specific)
	}
	c, err := newCursor(scope, "chapter", p.NextCursor)
	if err != nil || len(c.Courses) != 2 || c.Index != 1 || c.Courses[0].ref() != key("10", "101") {
		t.Fatal("saved discovery slots changed", c, err)
	}
	p, err = a.Sync(ctx, scope, p.Session, "chapter", p.NextCursor)
	if err != nil || len(p.Records) != 1 || p.Records[0].ParentID != key("10", "102") || specific != 1 || shared != 1 {
		t.Fatal("same name distinct term skipped", p, err, shared, specific)
	}
}

func TestExclusionDropsPendingBeforeBodyOrEmission(t *testing.T) {
	for _, kind := range []contract.Resource{"material", "discussion", "chapter"} {
		t.Run(string(kind), func(t *testing.T) {
			calls := 0
			a := testAdapter(func(r *http.Request) (*http.Response, error) {
				calls++
				if !strings.Contains(r.URL.Path, "getLastLearnedMocTermDto") {
					t.Fatal("pending body requested after exclusion", r.URL.Path)
				}
				if kind == "chapter" {
					rows := make([]map[string]any, 501)
					for i := range rows {
						rows[i] = map[string]any{"id": i + 1, "name": "Chapter"}
					}
					b, _ := json.Marshal(map[string]any{"code": 0, "result": map[string]any{"mocTermDto": map[string]any{"id": 202, "chapters": rows}}})
					return response(string(b)), nil
				}
				typ := 4
				if kind == "discussion" {
					typ = 6
				}
				return response(fmt.Sprintf(`{"code":0,"result":{"mocTermDto":{"id":202,"chapters":[{"id":1,"lessons":[{"id":2,"units":[{"id":3,"contentId":4,"contentType":%d,"name":"Body"}]}]}]}}}`, typ)), nil
			})
			s := sessionFor(t, a, scope, "fixture")
			co := course{ID: "10", Term: "101", Type: 2, Discovered: true, SourceTerm: "202", ActiveTerm: "202", ActiveRead: true}
			c, _ := newCursor(scope, kind, "")
			c.Courses, c.Done = []course{co}, true
			p, err := a.Sync(context.Background(), scope, s, kind, policyCursor(t, c))
			if err != nil || !p.HasMore || calls != 1 {
				t.Fatal("pending fixture", p, err, calls)
			}
			saved, err := newCursor(scope, kind, p.NextCursor)
			if err != nil || len(saved.Pending) != 1 || saved.PendingCourse.ref() != co.ref() {
				t.Fatal("pending owner", saved, err)
			}
			ctx := contract.WithExcludedCourses(context.Background(), scope, []string{co.ref()})
			p, err = a.Sync(ctx, scope, p.Session, kind, p.NextCursor)
			if err != nil || p.HasMore || len(p.Records) != 0 || calls != 1 {
				t.Fatal("pending read or emitted", p, err, calls)
			}
		})
	}
}

func TestExclusionAddedBetweenCourseMetadataPages(t *testing.T) {
	for _, after := range []int{1, 2} {
		t.Run(fmt.Sprintf("after page %d", after), func(t *testing.T) {
			calls := 0
			a := testAdapter(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method == http.MethodGet {
					return response(`<div class="learn-teacher-names">Teacher</div>`), nil
				}
				return response(`{"code":0,"result":{"mocTermDto":{"id":101,"chapters":[]}}}`), nil
			})
			s := sessionFor(t, a, scope, "fixture")
			co := course{ID: "10", Term: "101", Type: 1}
			c, _ := newCursor(scope, contract.Course, "")
			c.Courses, c.Done = []course{co}, true
			raw := policyCursor(t, c)
			for i := 0; i < after; i++ {
				p, err := a.Sync(context.Background(), scope, s, contract.Course, raw)
				if err != nil || !p.HasMore || len(p.Records) != 1 {
					t.Fatal("metadata fixture", p, err)
				}
				s, raw = p.Session, p.NextCursor
			}
			ctx := contract.WithExcludedCourses(context.Background(), scope, []string{co.ref()})
			p, err := a.Sync(ctx, scope, s, contract.Course, raw)
			if err != nil || p.HasMore || len(p.Records) != 0 || calls != after {
				t.Fatal("excluded metadata fetched or emitted", p, err, calls)
			}
		})
	}
}

func TestExclusionScopeAndExactRefBinding(t *testing.T) {
	co := course{ID: "10", Term: "101", Type: 1, Name: "Same name"}
	for _, tc := range []struct {
		name  string
		scope contract.Scope
		ref   string
	}{
		{"other user", contract.Scope{UserID: "other", ConnectionID: scope.ConnectionID}, co.ref()},
		{"other connection", contract.Scope{UserID: scope.UserID, ConnectionID: "other"}, co.ref()},
		{"other term", scope, key("10", "102")},
		{"other course", scope, key("11", "101")},
		{"raw JSON is not native ref", scope, `["10","101"]`},
		{"title is not native ref", scope, co.Name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := testAdapter(func(*http.Request) (*http.Response, error) {
				calls++
				return response(`{"code":0,"result":{"mocTermDto":{"id":101,"chapters":[{"id":1,"name":"Allowed"}]}}}`), nil
			})
			s := sessionFor(t, a, scope, "fixture")
			c, _ := newCursor(scope, "chapter", "")
			c.Courses, c.Done = []course{co}, true
			ctx := contract.WithExcludedCourses(context.Background(), tc.scope, []string{tc.ref})
			p, err := a.Sync(ctx, scope, s, "chapter", policyCursor(t, c))
			if err != nil || calls != 1 || len(p.Records) != 1 {
				t.Fatal("foreign policy blocked course", p, err, calls)
			}
		})
	}
}

func TestRemovingExclusionEnablesNextFullScan(t *testing.T) {
	shared, specific := 0, 0
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "getMyLearnedCoursePanelList") {
			shared++
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("courseType") == "2" {
				return response(`{"code":0,"result":{"result":[],"pagination":{"totalPageCount":0}}}`), nil
			}
			return response(`{"code":0,"result":{"result":[{"id":10,"name":"Owner","termPanel":{"id":101}}],"pagination":{"totalPageCount":1}}}`), nil
		}
		specific++
		return response(`{"code":0,"result":{"mocTermDto":{"id":101,"chapters":[{"id":1,"name":"Allowed"}]}}}`), nil
	})
	s := sessionFor(t, a, scope, "fixture")
	for _, blocked := range []bool{true, false} {
		ctx := context.Background()
		if blocked {
			ctx = contract.WithExcludedCourses(ctx, scope, []string{key("10", "101")})
		}
		raw, seen, finished := "", 0, false
		for i := 0; i < 6; i++ {
			p, err := a.Sync(ctx, scope, s, "chapter", raw)
			if err != nil {
				t.Fatal(err)
			}
			seen += len(p.Records)
			s, raw = p.Session, p.NextCursor
			if !p.HasMore {
				finished = true
				break
			}
		}
		want := 1
		if blocked {
			want = 0
		}
		if !finished || seen != want || specific != want {
			t.Fatal("full scan failed", blocked, finished, seen, specific)
		}
	}
	if shared != 4 {
		t.Fatal("discovery must remain allowed during exclusion", shared)
	}
}
