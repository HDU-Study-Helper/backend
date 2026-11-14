package runcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

func courseScreenBatch(n, size int) model.ControlBatch {
	b := model.ControlBatch{ID: "batch", OwnerID: "owner", RoundID: "round", Source: "mooc", Scope: &model.ControlBatchScope{Kind: "course", CourseRef: "course", ConnectionID: "connection"}}
	for i := 0; i < n; i++ {
		b.Changes = append(b.Changes, model.ControlChange{ID: fmt.Sprintf("c-%04d", i), Source: "mooc", Resource: "material", Before: mustJSON(map[string]any{"text": "original"}), After: mustJSON(map[string]any{"text": strings.Repeat("x", size), "index": i})})
	}
	return b
}

func TestTriggerPagesKeepWholeCourseEvidence(t *testing.T) {
	for _, size := range []int{1, 1300, 70000} {
		b := courseScreenBatch(734, size)
		pages, err := triggerPages(b)
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for i, page := range pages {
			if len(page.changes) > 100 || len(page.input) > triggerPageBytes {
				t.Fatal("unbounded page")
			}
			var payload struct {
				Page    map[string]int          `json:"page"`
				Scope   model.ControlBatchScope `json:"scope"`
				Unit    string                  `json:"processing_unit"`
				Changes []struct {
					ID            string `json:"change_id"`
					Before, After json.RawMessage
				} `json:"changes"`
			}
			if err := json.Unmarshal(page.input, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Page["index"] != i+1 || payload.Page["count"] != len(pages) || payload.Page["change_offset"] != seen || payload.Page["total_changes"] != 734 || payload.Scope.CourseRef != "course" || payload.Unit != "course" {
				t.Fatal("missing course/page identity")
			}
			for _, change := range payload.Changes {
				original := b.Changes[seen]
				if change.ID != original.ID || string(change.After) != string(original.After) || string(change.Before) != string(original.Before) {
					t.Fatal("evidence dropped or altered", seen)
				}
				seen++
			}
		}
		if seen != 734 {
			t.Fatal("missing changes", seen)
		}
	}
	b := courseScreenBatch(1, triggerPageBytes)
	if _, err := triggerPages(b); err == nil {
		t.Fatal("oversized single event accepted")
	}
}

func TestScreenTriggerRuntimeAggregatesAndCorrectsOnlyCurrentPage(t *testing.T) {
	for _, decisions := range [][]string{{"IGNORE", "UNCERTAIN", "TRIGGER"}, {"IGNORE", "UNCERTAIN", "IGNORE"}, {"IGNORE", "IGNORE", "IGNORE"}} {
		svc, _, store, _, _ := newItemsIntegration(t, "")
		b := courseScreenBatch(205, 5)
		calls, seen := 0, 0
		correction := false
		svc.Agents.Runtime.Providers["trigger"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
			calls++
			input := itemsUserInput(t, r)
			if len(mustJSON(input)) > triggerPageBytes {
				t.Fatal("correction too large")
			}
			var page map[string]int
			_ = json.Unmarshal(input["page"], &page)
			var changes []struct {
				ID string `json:"change_id"`
			}
			_ = json.Unmarshal(input["changes"], &changes)
			if len(changes) > 100 || page["count"] != 3 {
				t.Fatal("wrong page")
			}
			if page["index"] == 2 && len(input["validation_error"]) == 0 {
				return itemsFinal(map[string]any{"decision": "TRIGGER", "reason": "invented", "relevant_change_ids": []string{"c-0000"}}), nil
			}
			if page["index"] == 2 {
				correction = true
			}
			if page["change_offset"] != seen {
				t.Fatal("wrong evidence in correction")
			}
			seen += len(changes)
			ids := []string{}
			decision := decisions[page["index"]-1]
			if decision != "IGNORE" {
				ids = []string{changes[len(changes)-1].ID, changes[0].ID}
			}
			return itemsFinal(map[string]any{"decision": decision, "reason": "page evidence", "relevant_change_ids": ids}), nil
		})
		svc.process(context.Background(), b)
		if calls != 4 || seen != 205 || !correction || len(store.runs) != 4 {
			t.Fatal("fresh runtime/correction missing", calls, seen, len(store.runs))
		}
		got := store.batches[b.ID]
		decision, err := validateTrigger(got.Trigger, b.Changes)
		if err != nil {
			t.Fatal(err, got)
		}
		want := "IGNORE"
		for _, d := range decisions {
			if d == "TRIGGER" || d == "UNCERTAIN" && want == "IGNORE" {
				want = d
			}
		}
		if decision.Decision != want || !strings.Contains(decision.Reason, "205") {
			t.Fatal("false aggregate", decision)
		}
		if want == "TRIGGER" && !reflect.DeepEqual(decision.IDs, []string{"c-0100", "c-0199", "c-0200", "c-0204"}) {
			t.Fatal("wrong union/order", decision.IDs)
		}
		if want == "IGNORE" && got.Status != "ignored" || want != "IGNORE" && (got.Stage != "items" || got.Status != "queued") {
			t.Fatal("phase changed", got)
		}
	}
}

func TestScreenTriggerFailsAfterOneCorrection(t *testing.T) {
	svc, _, store, _, _ := newItemsIntegration(t, "")
	calls := 0
	svc.Agents.Runtime.Providers["trigger"] = itemsIntegrationModel(func(_ context.Context, _ f.ModelRequest) (f.ModelResponse, error) {
		calls++
		return itemsFinal(map[string]any{"decision": "bad"}), nil
	})
	svc.process(context.Background(), courseScreenBatch(205, 1))
	if calls != 2 || store.batches["batch"].Status != "failed" {
		t.Fatal("unbounded correction or hidden failure", calls)
	}
}

func TestLegacyTriggerRuntimeSingleCallAndBudget(t *testing.T) {
	for _, source := range []string{"dws", "ucampus"} {
		svc, _, store, _, _ := newItemsIntegration(t, "")
		b := courseScreenBatch(205, 1)
		b.Source = source
		calls := 0
		svc.Agents.Runtime.Providers["trigger"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
			calls++
			input := itemsUserInput(t, r)
			if len(input) != 2 || len(input["page"]) > 0 {
				t.Fatal("legacy shape changed")
			}
			return itemsFinal(map[string]any{"decision": "IGNORE", "reason": "legacy", "relevant_change_ids": []string{}}), nil
		})
		// This fixture exceeds the legacy bound, so it must fail before calling.
		b.Changes = b.Changes[:1]
		svc.process(context.Background(), b)
		if calls != 1 || store.batches[b.ID].Status != "ignored" {
			t.Fatal("legacy did not use one call")
		}
		b.Changes[0].After = mustJSON(map[string]any{"text": strings.Repeat("x", 49<<10)})
		if _, err := triggerPages(b); err == nil {
			t.Fatal("legacy budget relaxed")
		}
	}
}

func TestTriggerOutputNormalizesOrder(t *testing.T) {
	out, err := validateTrigger([]byte(`{"decision":"TRIGGER","reason":"valid","relevant_change_ids":["b","a"]}`), []model.ControlChange{{ID: "a"}, {ID: "b"}})
	if err != nil || !reflect.DeepEqual(out.IDs, []string{"a", "b"}) {
		t.Fatal("order not normalized", out, err)
	}
}
