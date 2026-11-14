package dbbridge

import (
	"academic/backend/internal/model"
	"encoding/json"
	"testing"
)

func TestMOOCCatalogSourcesRemainScopedAndSanitized(t *testing.T) {
	ref := academicRef("mooc", "c", "", "route")
	fields := map[string]any{"courseId": "c", "termId": "route", "sourceTermId": "source", "catalog_sources": []any{map[string]any{"sourceTermId": "route", "source_scope": "route", "catalog_chapter_count": 0, "course_introduction": "Route intro", "requirements": "Route requirement", "cookie": "SECRET"}, map[string]any{"sourceTermId": "source", "source_scope": "active_source", "catalog_chapter_count": 6, "teachers": []any{"Teacher"}, "endTime": 1793728361254, "profile": map[string]any{"phone": "SECRET"}}}}
	p, e := buildAcademicProjection("o", "conn", "mooc", []model.PlatformRecord{academicFixture(1, "mooc", "course", ref, "", "Course", fields)})
	must(t, e)
	var details map[string]any
	json.Unmarshal(p.offerings[0].Details, &details)
	sources := details["catalog_sources"].([]any)
	if len(sources) != 2 {
		t.Fatal(details)
	}
	route := sources[0].(map[string]any)
	active := sources[1].(map[string]any)
	if route["sourceTermId"] != "route" || route["catalog_chapter_count"] != float64(0) || active["sourceTermId"] != "source" || active["catalog_chapter_count"] != float64(6) {
		t.Fatal("source-scoped metadata lost", sources)
	}
	if _, ok := route["cookie"]; ok {
		t.Fatal("credential escaped")
	}
	if _, ok := active["profile"]; ok {
		t.Fatal("full profile escaped")
	}
}

func TestMOOCRosterCourseResolvedByStoredTopicWithoutGuessing(t *testing.T) {
	ref := academicRef("mooc", "course", "", "route")
	fields := map[string]any{"courseId": "course", "termId": "route", "sourceTermId": "source"}
	rows := []model.PlatformRecord{academicFixture(1, "mooc", "course", ref, "", "Course", fields), academicFixture(2, "mooc", "discussion", "topic", ref, "Question", fields), academicFixture(3, "mooc", "discussion_reply_user", "reply", "topic", "", map[string]any{"termId": "route", "sourceTermId": "source", "is_current_user": false})}
	p, e := buildAcademicProjection("owner", "conn", "mooc", rows)
	must(t, e)
	for _, i := range p.items {
		if i.Kind == "discussion_reply_user" && (i.OfferingID == "" || i.NativeCourseID != "course" || i.NativeTermID != "route" || i.SourceTermID != "source") {
			t.Fatal("explicit parent topic association lost", i)
		}
	}
	rows[2].ParentID = "unknown"
	p, e = buildAcademicProjection("owner", "conn", "mooc", rows)
	must(t, e)
	for _, i := range p.items {
		if i.Kind == "discussion_reply_user" && i.OfferingID != "" {
			t.Fatal("guessed course without verified root", i)
		}
	}
}

func TestMOOCProgressAndRosterKeepOwnKindsAndCourseState(t *testing.T) {
	ref := academicRef("mooc", "c", "", "route")
	fields := func(extra map[string]any) map[string]any {
		f := map[string]any{"courseId": "c", "termId": "route", "sourceTermId": "source", "contentType": 6, "chapterId": "ch", "lessonId": "ls", "unitId": "u"}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	roster := academicFixture(3, "mooc", "discussion_reply_user", "reply", "topic", "", fields(map[string]any{"topic_id": "topic", "replyTime": 1793728361254, "is_current_user": true, "source_list_endpoint": "PostBean.getPaginationReplys", "body_status": "not_collected"}))
	var raw map[string]any
	json.Unmarshal(roster.Content, &raw)
	raw["content"] = ""
	roster.Content = academicJSON(raw)
	rows := []model.PlatformRecord{academicFixture(1, "mooc", "course", ref, "", "Course", fields(nil)), academicFixture(2, "mooc", "progress", "progress", ref, "Progress", fields(map[string]any{"userSubmitStatus": 0, "userEffectStatus": false, "totalLearnedTime": 0, "voteCount": 0})), roster}
	p, e := buildAcademicProjection("owner", "conn", "mooc", rows)
	must(t, e)
	if len(p.items) != 2 {
		t.Fatal(p.items)
	}
	for _, i := range p.items {
		if i.OfferingID == "" || i.SourceTermID != "source" {
			t.Fatal("course attribution lost", i)
		}
		switch i.NativeID {
		case "progress":
			if i.Kind != "progress" {
				t.Fatal("discussion contentType overwrote progress kind", i)
			}
			var s map[string]any
			json.Unmarshal(i.PersonalState, &s)
			for _, k := range []string{"userSubmitStatus", "userEffectStatus", "totalLearnedTime", "voteCount"} {
				if _, ok := s[k]; !ok {
					t.Fatal("lost personal source field", k, s)
				}
			}
		case "reply":
			if i.Kind != "discussion_reply_user" || i.Published.Value == nil || i.Text != "" {
				t.Fatal("roster semantics lost", i)
			}
			var m map[string]any
			json.Unmarshal(i.Metadata, &m)
			if m["topic_id"] != "topic" || m["source_list_endpoint"] != "PostBean.getPaginationReplys" {
				t.Fatal("roster relationship or source lost", m)
			}
		}
	}
}
