package mooc

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestOfficialCourseMetadataRejectsGenericPageAndPreservesText(t *testing.T) {
	body := `window.courseDto={id:10};<div class="m-infomation_content-section"><h2>课程概述</h2><p>Read this introduction</p><script>never()</script></div>`
	f, e := extractCourseInfo(body, "10")
	if e != nil || !strings.Contains(text(f["course_introduction"]), "Read this introduction") || strings.Contains(text(f["course_introduction"]), "never") {
		t.Fatal(f, e)
	}
	f, e = extractCourseInfo(body, "11")
	if e != nil || text(f["course_introduction"]) != "" {
		t.Fatal("wrong course intro accepted", f, e)
	}
	f, e = extractLearningTeachers(`<div class="learn-teacher-names-wrapper"><div class="learn-teacher-names">Teacher One Teacher Two</div></div>`)
	if e != nil || len(f["teachers"].([]string)) != 1 {
		t.Fatal(f, e)
	}
}
func TestCourseMetadataCursorScansRouteAndActiveInfo(t *testing.T) {
	calls := []string{}
	a := testAdapter(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.Path, "/spoc/learn/") {
			calls = append(calls, "learn")
			return response(`window.courseDto={id:10};window.termDto={id:101};window.moocTermDto={id:202};<div class="learn-teacher-names">Teacher</div>`), nil
		}
		if strings.HasPrefix(req.URL.Path, "/course/") {
			calls = append(calls, "info")
			id := "10"
			if strings.Contains(req.URL.Path, "-20") {
				id = "20"
			}
			return response(`window.courseDto={id:` + id + `};<div class="m-infomation_content-section">Introduction ` + id + `</div>`), nil
		}
		req.ParseForm()
		term := req.Form.Get("termId")
		calls = append(calls, term)
		id := "10"
		if term == "202" {
			id = "20"
		}
		return response(`{"code":0,"result":{"mocTermDto":{"id":` + term + `,"courseId":` + id + `,"chapters":[]}}}`), nil
	})
	s := sessionFor(t, a, scope, "fixture")
	c, _ := newCursor(scope, contract.Course, "")
	c.Done = true
	c.Courses = []course{{ID: "10", Term: "101", Type: 2, Name: "Owner", School: "TEST"}}
	c.SourceSchools = map[string]string{"20": "SOURCE"}
	b, _ := json.Marshal(c)
	raw := base64Cursor(b)
	last := contract.Record{}
	for i := 0; i < 5; i++ {
		p, e := a.Sync(context.Background(), scope, s, contract.Course, raw)
		if e != nil {
			t.Fatal(e)
		}
		if len(p.Records) > 0 {
			last = p.Records[len(p.Records)-1]
		}
		if i < 4 && !p.HasMore {
			t.Fatal("source scope skipped")
		}
		if i == 4 && p.HasMore {
			t.Fatal("did not finish")
		}
		raw = p.NextCursor
		s = p.Session
	}
	if strings.Join(calls, ",") != "learn,101,info,202,info" || last.ParentID != "" || last.ExternalID != key("10", "101") || last.Content != "Introduction 20" || last.Fields["teacher_coverage"] != "official_learning_page_display_names" {
		t.Fatal(calls, last)
	}
}
