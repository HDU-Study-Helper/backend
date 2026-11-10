package mooc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func fixtureDTO(t *testing.T, s string) map[string]any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var m map[string]any
	if e := d.Decode(&m); e != nil {
		t.Fatal(e)
	}
	return m
}

func TestCatalogNativeEnumsHierarchyAndMetadata(t *testing.T) {
	// Sanitized official shapes: assessment collections and plain units use
	// distinct enum sets. Source term IDs must remain separate from route ownership.
	dto := fixtureDTO(t, `{"mocTermDto":{"id":202,"chapters":[{"id":11,"name":"Chapter","releaseTime":100,"lessons":[{"id":12,"name":"Lesson","units":[{"id":13,"name":"Discussion","contentType":6,"contentId":90,"completePercent":1,"replyCount":2},{"id":14,"name":"Document","contentType":3,"contentId":91,"contentFileName":"slides.pdf"},{"id":15,"name":"Legacy assessment","contentType":5,"contentId":92},{"id":16,"name":"Video","contentType":1,"contentId":93,"durationInSeconds":500},{"id":17,"name":"Exam in test context","contentType":6,"test":{"id":95,"type":6,"totalScore":100,"testTime":3600,"trytime":2,"questions":[{"answer":"never"}]}}]}],"quizs":[{"id":18,"contentId":94,"contentType":2,"name":"Quiz","gmtCreate":10,"gmtModified":20,"releaseTime":30,"test":{"type":2,"totalScore":30,"testTime":900,"trytime":2,"usedTryCount":0}}]}],"exams":[{"id":19,"name":"Explicit exam","chapterId":11,"contentType":4,"questions":[{"answer":"never"}]}]}}`)
	co := course{ID: "10", Term: "101", SourceTerm: "202", ActiveTerm: "202", Type: 2, Name: "Owner course"}
	for kind, want := range map[contract.Resource]int{"chapter": 1, "lesson": 1, "discussion": 1, "material": 3, contract.Assignment: 1, contract.Exam: 2, contract.Progress: 2} {
		rows, e := termRecords(dto, co, kind)
		if e != nil || len(rows) != want {
			t.Fatalf("%s count=%d want=%d err=%v", kind, len(rows), want, e)
		}
		for _, row := range rows {
			if row.ParentID != co.ref() || row.Fields["course_name"] != "Owner course" || row.Fields["sourceTermId"] != "202" || row.Fields["source_scope"] != "active_source" {
				t.Fatal("ownership/source lost", row)
			}
			if row.Fields["source_collection"] != "exams" && row.Fields["chapterId"] != "11" {
				t.Fatal("chapter path lost", row)
			}
		}
		raw, _ := json.Marshal(rows)
		if strings.Contains(string(raw), "never") || strings.Contains(string(raw), "questions") {
			t.Fatal("assessment content escaped")
		}
	}
	rows, _ := termRecords(dto, co, contract.Assignment)
	f := rows[0].Fields
	for _, k := range []string{"gmtCreate", "gmtModified", "releaseTime", "totalScore", "testTime", "trytime"} {
		if _, ok := f[k]; !ok {
			t.Fatal("missing source scalar", k)
		}
	}
	co.SourceTerm = "101"
	if _, e := termRecords(dto, co, "chapter"); !errors.Is(e, contract.ErrParse) {
		t.Fatal("source mismatch accepted", e)
	}
}
func TestSpocDiscoveryKeepsRouteAndRejectsAmbiguity(t *testing.T) {
	co := course{ID: "10", Term: "101", Type: 2}
	page := `window.courseDto={id:"10",name:"Never evaluate"}; window.termDto={id:"101",name:"route"}; window.moocTermDto={id:"202"};`
	got, e := discoverPage(page, co)
	if e != nil || got.Term != "101" || got.ActiveTerm != "202" {
		t.Fatal(got, e)
	}
	for _, bad := range []string{`window.moocTermDto={id:202};window.moocTermDto={id:203};`, `window.courseDto={activeTermId:203};window.moocTermDto={id:202};`, `window.moocTermDto={id:202`} {
		if _, e := discoverPage(bad, co); !errors.Is(e, contract.ErrParse) {
			t.Fatal("ambiguous metadata accepted", bad, e)
		}
	}
	got, e = discoverPage(`window.termDto={id:"101"};`, co)
	if e != nil || got.ActiveTerm != "" || !strings.HasPrefix(got.Discovery, "unsupported:") {
		t.Fatal(got, e)
	}
}
func TestSpocSyncScansBothTermsWithOriginalReferer(t *testing.T) {
	calls := []string{}
	a := testAdapter(func(req *http.Request) (*http.Response, error) {
		if req.Method == "GET" {
			calls = append(calls, "page")
			return response(`window.courseDto={id:"10"};window.termDto={id:"101"};window.moocTermDto={id:"202"};`), nil
		}
		if strings.Contains(req.URL.Path, "PostBean.getPostDetailById") {
			calls = append(calls, "detail")
			return response(`dwr.engine._remoteHandleCallback('0','0',{id:14,hasFollowed:false});`), nil
		}
		if strings.Contains(req.URL.Path, "PostBean.getPaginationReplys") {
			calls = append(calls, "replies")
			return response(`dwr.engine._remoteHandleCallback('0','0',{list:[],pageNo:1,totalCount:0,totalPageCount:0});`), nil
		}
		if strings.Contains(req.URL.Path, "getLessonUnitLearnVo") {
			calls = append(calls, "body")
			return response(`dwr.engine._remoteHandleCallback('0','0',{unitId:13,post:{id:14,content:"Discussion body"}});`), nil
		}
		req.ParseForm()
		term := req.Form.Get("termId")
		calls = append(calls, term)
		if req.Header.Get("Referer") != origin+"/spoc/learn/10?tid=101" {
			t.Fatal("route owner changed")
		}
		return response(`{"code":0,"result":{"mocTermDto":{"id":` + term + `,"chapters":[{"id":11,"lessons":[{"id":12,"units":[{"id":13,"contentId":14,"contentType":6}]}]}]}}}`), nil
	})
	s := sessionFor(t, a, scope, "fixture")
	c, _ := newCursor(scope, "discussion", "")
	c.Done = true
	c.Courses = []course{{ID: "10", Term: "101", Type: 2, Name: "Owner"}}
	b, _ := json.Marshal(c)
	raw := base64Cursor(b)
	rows := []contract.Record{}
	for i := 0; i < 9; i++ {
		p, e := a.Sync(context.Background(), scope, s, "discussion", raw)
		if e != nil {
			t.Fatal(e)
		}
		rows = append(rows, p.Records...)
		raw = p.NextCursor
		s = p.Session
		if i < 8 && !p.HasMore {
			t.Fatal("lost source term")
		}
		if i == 8 && p.HasMore {
			t.Fatal("did not finish")
		}
	}
	if strings.Join(calls, ",") != "page,101,body,detail,replies,202,body,detail,replies" || len(rows) != 2 || rows[0].ExternalID == rows[1].ExternalID || rows[0].ParentID != rows[1].ParentID {
		t.Fatal(calls, rows)
	}
}
