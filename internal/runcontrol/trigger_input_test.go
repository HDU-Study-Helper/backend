package runcontrol

import (
	"academic/backend/internal/model"
	"encoding/json"
	"fmt"
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
		Scope             model.ControlBatchScope `json:"scope"`
		Unit              string                  `json:"processing_unit"`
		Refresh           string                  `json:"refresh_id"`
		SelectionProtocol string                  `json:"selection_protocol"`
		Changes           []struct {
			Index       int             `json:"index"`
			Resource    string          `json:"resource"`
			After       json.RawMessage `json:"after"`
			ChangeID    json.RawMessage `json:"change_id"`
			EvidenceIDs json.RawMessage `json:"evidence_ids"`
		} `json:"changes"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatal("invalid input")
	}
	if payload.Unit != "course" || payload.Scope.CourseRef != scope.CourseRef || payload.Refresh != "round" || payload.SelectionProtocol != "indices_v1" || len(payload.Changes) != 3 {
		t.Fatal("course input lost scope or changes")
	}
	for i, change := range payload.Changes {
		if change.Index != i+1 || change.Resource != b.Changes[i].Resource || string(change.After) != string(b.Changes[i].After) || len(change.ChangeID) != 0 || len(change.EvidenceIDs) != 0 {
			t.Fatal("course input lost content or exposed internal IDs", i)
		}
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
		if len(payload) != 5 || payload["selection_count"] != float64(1) || payload["selection_protocol"] != "indices_v1" || payload["scope"] != nil || payload["processing_unit"] != nil {
			t.Fatal("non-course platform input changed", source, string(raw))
		}
		changes, ok := payload["changes"].([]any)
		if !ok || len(changes) != 1 {
			t.Fatal("non-course changes lost", source, string(raw))
		}
		change, ok := changes[0].(map[string]any)
		if !ok || change["index"] != float64(1) || change["change_id"] != nil || change["evidence_ids"] != nil {
			t.Fatal("non-course indices missing or internal IDs exposed", source, string(raw))
		}
	}
}
func TestTriggerSelectionBoundsDoNotUsePageNumber(t *testing.T) {
	for _, count := range []int{0, 1, 7} {
		b := model.ControlBatch{ID: "batch", Source: "mooc", Scope: &model.ControlBatchScope{Kind: "course", ConnectionID: "connection"}}
		for i := 0; i < count; i++ {
			b.Changes = append(b.Changes, model.ControlChange{ID: fmt.Sprintf("change-%d", i)})
		}
		raw, err := encodeTriggerInput(b, map[string]int{"index": 8, "count": 8, "total_changes": 342})
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Count  int            `json:"selection_count"`
			Bounds map[string]int `json:"selection_bounds"`
			Page   map[string]int `json:"page"`
		}
		if err = json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Count != count || payload.Bounds["min"] != 1 || payload.Bounds["max"] != count || payload.Page["index"] != 8 || payload.Page["count"] != 8 {
			t.Fatal("pagination leaked into selectors", string(raw))
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
