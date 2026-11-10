package dbbridge

import (
	f "academic/agent/framework"
	"encoding/json"
	"testing"
)

func TestInitialCourseProfileRejectsUnfinishedSurvey(t *testing.T) {
	w := f.Workflow{Definition: json.RawMessage(`{"entity":{"kind":"course"}}`)}
	seeds := []f.Memory{{Category: "profile_index", Content: "index"}, {Category: "identity", Content: "facts"}}
	if validateInitialCourseProfile(w, seeds) == nil {
		t.Fatal("missing manifest accepted")
	}
	p := map[string]any{"version": 1, "keywords": []string{"数分"}, "search_coverage": []any{map[string]any{"dataset": "schedules", "complete": false}}, "dimensions": map[string]any{}}
	for _, k := range []string{"identity", "timetable", "teaching_progress", "assignments", "materials", "discussion_participation"} {
		p["dimensions"].(map[string]any)[k] = map[string]any{"status": "partial"}
	}
	raw, _ := json.Marshal(map[string]any{"entity": map[string]string{"kind": "course"}, "profile_manifest": p})
	w.Definition = raw
	if validateInitialCourseProfile(w, seeds) == nil {
		t.Fatal("incomplete accepted")
	}
	p["search_coverage"] = []any{}
	for _, dataset := range []string{"platform_records", "academic_offerings", "academic_items", "schedules", "dws_messages", "dws_attachments"} {
		p["search_coverage"] = append(p["search_coverage"].([]any), map[string]any{"dataset": dataset, "complete": true})
	}
	raw, _ = json.Marshal(map[string]any{"entity": map[string]string{"kind": "course"}, "profile_manifest": p})
	w.Definition = raw
	if e := validateInitialCourseProfile(w, seeds); e != nil {
		t.Fatal(e)
	}
}
