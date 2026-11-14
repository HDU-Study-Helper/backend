package mooc

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestCourseSourceSnapshotsDoNotInheritPreviousTerm(t *testing.T) {
	a := testAdapter(func(req *http.Request) (*http.Response, error) {
		if strings.HasPrefix(req.URL.Path, "/course/") {
			id := "10"
			if strings.Contains(req.URL.Path, "SOURCE-20") {
				id = "20"
			}
			return response(`window.courseDto={id:` + id + `};<div class="m-infomation_content-section">Introduction ` + id + `</div>`), nil
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if req.Form.Get("termId") == "101" {
			return response(`{"code":0,"result":{"mocTermDto":{"id":101,"courseId":10,"releaseTime":100,"endTime":200,"requirements":"Route requirements","chiefLectorDto":{"name":"Route teacher","phone":"PROFILE_SECRET"},"chapters":[]}}}`), nil
		}
		return response(`{"code":0,"result":{"mocTermDto":{"id":202,"courseId":20,"releaseTime":300,"chapters":[{"id":1},{"id":2}]}}}`), nil
	})
	c, _ := newCursor(scope, contract.Course, "")
	c.Done = true
	c.Courses = []course{{ID: "10", Term: "101", Type: 2, School: "ROUTE", Discovered: true, ActiveTerm: "202"}}
	c.SourceSchools = map[string]string{"20": "SOURCE"}
	s := sessionFor(t, a, scope, "fixture")
	raw := policyCursor(t, c)
	var last contract.Record
	for i := 0; i < 4; i++ {
		p, err := a.Sync(context.Background(), scope, s, contract.Course, raw)
		if err != nil || len(p.Records) != 1 || p.HasMore != (i < 3) {
			t.Fatal(i, p, err)
		}
		last = p.Records[0]
		raw, s = p.NextCursor, p.Session
	}
	f := last.Fields
	if f["sourceTermId"] != "202" || f["source_scope"] != "active_source" || f["sourceCourseId"] != "20" || text(f["releaseTime"]) != "300" || text(f["catalog_chapter_count"]) != "2" || f["catalog_status"] != "observed" || last.Content != "Introduction 20" {
		t.Fatal("stale route metadata overwrote active source", last)
	}
	for _, k := range []string{"endTime", "requirements", "teachers", "teacher_coverage"} {
		if _, exists := f[k]; exists {
			t.Fatal("absent source field inherited route value", k, f[k])
		}
	}
	sources := f["catalog_sources"].([]any)
	if len(sources) != 2 {
		t.Fatal(sources)
	}
	route, active := object(sources[0]), object(sources[1])
	if route["sourceTermId"] != "101" || text(route["endTime"]) != "200" || route["requirements"] != "Route requirements" || route["course_introduction"] != "Introduction 10" || route["teacher_coverage"] != "catalog_lector_display_names" || active["sourceTermId"] != "202" {
		t.Fatal("source snapshot lost", sources)
	}
	wire, _ := json.Marshal(last)
	if strings.Contains(string(wire), "PROFILE_SECRET") || strings.Contains(string(wire), "chiefLectorDto") {
		t.Fatal("full teacher profile escaped")
	}
}

func TestCourseRecordRestoredProjectionCannotOverrideOwner(t *testing.T) {
	co := course{ID: "10", Term: "101", SourceTerm: "202", Type: 2, Metadata: map[string]any{"sourceTermId": "101", "source_scope": "route", "courseId": "999", "body_status": "catalog_description", "course_introduction": "Safe introduction", "response": map[string]any{"private": "SECRET"}}}
	r := courseRecord(co)
	if r.Fields["courseId"] != "10" || r.Fields["sourceTermId"] != "202" || r.Fields["source_scope"] != "active_source" || r.Fields["body_status"] != "catalog_description" || r.Content != "Safe introduction" {
		t.Fatal(r)
	}
	if _, ok := r.Fields["response"]; ok {
		t.Fatal("unselected response retained")
	}
}

func TestUnknownAssessmentNeverReadsStandaloneUnitBody(t *testing.T) {
	for _, typ := range []string{"2", "3", "4", "6"} {
		for _, test := range []string{`{}`, `{"type":99}`} {
			dto := fixtureDTO(t, `{"id":101,"chapters":[{"id":1,"lessons":[{"id":2,"units":[{"id":3,"contentId":4,"contentType":`+typ+`,"test":`+test+`}]}]}]}`)
			rows, err := termRecords(dto, course{ID: "10", Term: "101", Type: 1}, "material")
			if err != nil || len(rows) != 1 || rows[0].Fields["classification_status"] != "unsupported_assessment_subtype" || needsUnitBody(rows[0]) {
				t.Fatal(typ, test, rows, err)
			}
			for _, kind := range []contract.Resource{contract.Assignment, contract.Exam, "discussion"} {
				rows, err := termRecords(dto, course{ID: "10", Term: "101", Type: 1}, kind)
				if err != nil || len(rows) != 0 {
					t.Fatal("unknown assessment guessed", typ, test, kind, rows, err)
				}
			}
		}
	}
}

func TestDiscussionDetailPartialProgressRetainsCatalogState(t *testing.T) {
	h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		return response(`dwr.engine._remoteHandleCallback('0','0',{id:14,relateUnit:{id:13,replyCount:0,completePercent:0}});`), nil
	})
	f := c.Discussion.Root.Fields
	f["userSubmitStatus"], f["replyCount"], f["completePercent"] = json.Number("0"), json.Number("2"), json.Number("1")
	progressCoverage(f)
	if _, err := advanceDiscussion(context.Background(), h, co, c); err != nil {
		t.Fatal(err)
	}
	native := object(f["native_progress_fields"])
	if text(native["userSubmitStatus"]) != "0" || text(native["replyCount"]) != "0" || text(native["completePercent"]) != "0" || text(f["completePercent"]) != "0" || f["progress_status"] != "source_fields_observed" {
		t.Fatal("partial detail erased catalog state or contradicted flat state", f)
	}
	if _, exists := object(f["detail_progress_fields"])["userSubmitStatus"]; exists {
		t.Fatal("catalog state misrepresented as detail evidence")
	}
}
