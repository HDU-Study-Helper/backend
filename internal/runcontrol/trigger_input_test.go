package runcontrol

import (
	"academic/backend/internal/model"
	"encoding/json"
	"strings"
	"testing"
)

func TestCourseTriggerInputKeepsAllResourcesTogether(t *testing.T) {
	scope := &model.ControlBatchScope{Kind: "course", ConnectionID: "connection", OfferingID: "offering", CourseRef: `["course","class"]`, CourseName: "具体课程"}
	b := model.ControlBatch{ID: "batch", RoundID: "round", Source: "chaoxing", Scope: scope}
	for _, kind := range []string{"assignment", "discussion", "material"} {
		b.Changes = append(b.Changes, model.ControlChange{ID: kind, Resource: kind, Source: "chaoxing", ConnectionID: "connection", After: json.RawMessage(`{"content":"` + strings.Repeat("x", 18000) + `"}`)})
	}
	raw, err := triggerInput(b)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Scope   model.ControlBatchScope `json:"scope"`
		Unit    string                  `json:"processing_unit"`
		Refresh string                  `json:"refresh_id"`
		Changes []model.ControlChange   `json:"changes"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatal("invalid input")
	}
	if payload.Unit != "course" || payload.Scope.CourseRef != scope.CourseRef || payload.Refresh != "round" || len(payload.Changes) != 3 {
		t.Fatal("course input lost scope or changes")
	}
	b.Scope = nil
	if _, err = triggerInput(b); err == nil {
		t.Fatal("legacy budget relaxed")
	}
}
func TestLegacyTriggerInputIsUnchangedForDWSAndUCampus(t *testing.T) {
	for _, source := range []string{"dws", "ucampus"} {
		b := model.ControlBatch{ID: "batch", RoundID: "round", Source: source, Scope: &model.ControlBatchScope{Kind: "course", CourseName: "must not inject"}, Changes: []model.ControlChange{{ID: "change", Resource: "message", Source: source, After: json.RawMessage(`{"text":"hello"}`)}}}
		raw, err := triggerInput(b)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if json.Unmarshal(raw, &payload) != nil {
			t.Fatal("bad input")
		}
		if len(payload) != 2 || payload["scope"] != nil || payload["processing_unit"] != nil {
			t.Fatal("legacy platform input changed", source, string(raw))
		}
	}
}
func TestCourseTriggerInputRejectsOverflowWithoutTruncation(t *testing.T) {
	b := model.ControlBatch{ID: "batch", Source: "mooc", Scope: &model.ControlBatchScope{Kind: "course", ConnectionID: "connection"}, Changes: []model.ControlChange{{ID: "change", After: json.RawMessage(`{"text":"` + strings.Repeat("x", 769<<10) + `"}`)}}}
	if _, err := triggerInput(b); err == nil {
		t.Fatal("overflow accepted")
	}
	if len(b.Changes) != 1 || len(b.Changes[0].After) < 769<<10 {
		t.Fatal("original evidence changed")
	}
}
