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
				Page              map[string]int          `json:"page"`
				Scope             model.ControlBatchScope `json:"scope"`
				Unit              string                  `json:"processing_unit"`
				SelectionProtocol string                  `json:"selection_protocol"`
				SelectionCount    int                     `json:"selection_count"`
				SelectionBounds   map[string]int          `json:"selection_bounds"`
				Changes           []struct {
					Index         int `json:"index"`
					Before, After json.RawMessage
				} `json:"changes"`
			}
			if err := json.Unmarshal(page.input, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Page["index"] != i+1 || payload.Page["count"] != len(pages) || payload.Page["change_offset"] != seen || payload.Page["total_changes"] != 734 || payload.Scope.CourseRef != "course" || payload.Unit != "course" || payload.SelectionProtocol != "indices_v1" || payload.SelectionCount != len(page.changes) || payload.SelectionBounds["min"] != 1 || payload.SelectionBounds["max"] != len(page.changes) {
				t.Fatal("missing course/page identity")
			}
			for j, change := range payload.Changes {
				original := b.Changes[seen]
				if change.Index != j+1 || string(change.After) != string(original.After) || string(change.Before) != string(original.Before) {
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
				Index int `json:"index"`
			}
			_ = json.Unmarshal(input["changes"], &changes)
			if len(changes) > 100 || page["count"] != 3 || string(input["selection_protocol"]) != `"indices_v1"` {
				t.Fatal("wrong page")
			}
			for i, change := range changes {
				if change.Index != i+1 {
					t.Fatal("indices not local to page")
				}
			}
			if page["index"] == 2 && len(input["validation_error"]) == 0 {
				return itemsFinal(map[string]any{"decision": "TRIGGER", "reason": "out of range", "relevant_indices": []int{0}}), nil
			}
			if page["index"] == 2 {
				correction = true
			}
			if page["change_offset"] != seen {
				t.Fatal("wrong evidence in correction")
			}
			seen += len(changes)
			indices := []int{}
			decision := decisions[page["index"]-1]
			if decision != "IGNORE" {
				indices = []int{changes[0].Index, changes[len(changes)-1].Index}
			}
			return itemsFinal(map[string]any{"decision": decision, "reason": "page evidence", "relevant_indices": indices}), nil
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
			if len(input) != 5 || string(input["selection_count"]) != "1" || len(input["page"]) > 0 || string(input["selection_protocol"]) != `"indices_v1"` {
				t.Fatal("non-course input shape changed")
			}
			return itemsFinal(map[string]any{"decision": "IGNORE", "reason": "legacy", "relevant_indices": []int{}}), nil
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

func TestTriggerIndicesMapToChangeIDs(t *testing.T) {
	changes := []model.ControlChange{{ID: "page-first"}, {ID: "page-middle"}, {ID: "page-last"}}
	out, err := validateTriggerIndices([]byte(`{"decision":"TRIGGER","reason":"valid","relevant_indices":[1,3]}`), changes)
	if err != nil || !reflect.DeepEqual(out.IDs, []string{"page-first", "page-last"}) {
		t.Fatal("indices not mapped to page change IDs", out, err)
	}
}

func TestTriggerIndicesRejectInvalidSelections(t *testing.T) {
	changes := []model.ControlChange{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	for _, tc := range []struct {
		name    string
		indices string
	}{
		{name: "zero", indices: `[0]`},
		{name: "negative", indices: `[-1]`},
		{name: "past page", indices: `[4]`},
		{name: "duplicate", indices: `[1,1]`},
		{name: "out of order", indices: `[3,1]`},
		{name: "fractional", indices: `[1.5]`},
		{name: "string", indices: `["1"]`},
		{name: "null list", indices: `null`},
		{name: "null element", indices: `[null]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"decision":"TRIGGER","reason":"invalid selection","relevant_indices":` + tc.indices + `}`)
			if _, err := validateTriggerIndices(raw, changes); err == nil {
				t.Fatal("invalid indices accepted", tc.indices)
			}
		})
	}
}

func TestScreenTriggerLastPageCorrectionUsesActualSelectionCount(t *testing.T) {
	for _, correct := range []bool{true, false} {
		t.Run(fmt.Sprintf("correction_valid_%t", correct), func(t *testing.T) {
			svc, _, _, _, _ := newItemsIntegration(t, "")
			// 707 small changes produce eight pages: the final page is page 8,
			// but contains only seven selectable changes, matching the live failure.
			b := courseScreenBatch(707, 1)
			calls, corrections := 0, 0
			svc.Agents.Runtime.Providers["trigger"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
				calls++
				input := itemsUserInput(t, r)
				var page map[string]int
				_ = json.Unmarshal(input["page"], &page)
				if page["index"] != 8 {
					return itemsFinal(map[string]any{"decision": "IGNORE", "reason": "irrelevant", "relevant_indices": []int{}}), nil
				}
				var count int
				var bounds map[string]int
				_ = json.Unmarshal(input["selection_count"], &count)
				_ = json.Unmarshal(input["selection_bounds"], &bounds)
				if page["count"] != 8 || count != 7 || bounds["max"] != 7 {
					t.Fatal("last page selectors incorrect", string(mustJSON(input)))
				}
				if len(input["validation_error"]) > 0 {
					corrections++
					var feedback string
					_ = json.Unmarshal(input["validation_error"], &feedback)
					for _, want := range []string{"relevant_indices[7]=8", "[1,7]", "7 changes", "selection_count=7", "page.index/page.count"} {
						if !strings.Contains(feedback, want) {
							t.Fatalf("missing specific correction %q in %s", want, feedback)
						}
					}
					if correct {
						return itemsFinal(map[string]any{"decision": "TRIGGER", "reason": "valid last page", "relevant_indices": []int{1, 2, 3, 4, 5, 6, 7}}), nil
					}
				}
				return itemsFinal(map[string]any{"decision": "TRIGGER", "reason": "page number confused with selector", "relevant_indices": []int{1, 2, 3, 4, 5, 6, 7, 8}}), nil
			})
			out, _, err := svc.screenTrigger(context.Background(), b, "trigger")
			if calls != 9 || corrections != 1 {
				t.Fatal("correction not bounded to current page", calls, corrections)
			}
			if !correct {
				if err == nil || !strings.Contains(err.Error(), "relevant_indices[7]=8") {
					t.Fatal("out-of-range selection accepted", out, err)
				}
				return
			}
			want := []string{}
			for _, change := range b.Changes[700:] {
				want = append(want, change.ID)
			}
			if err != nil || !reflect.DeepEqual(out.IDs, want) {
				t.Fatal("wrong final page mapping", out, err)
			}
		})
	}
}

func TestTriggerCorrectionMessageIsBoundedAndKeepsDecisionRules(t *testing.T) {
	message := triggerCorrectionMessage(f.Fail("INVALID_TRIGGER_OUTPUT", strings.Repeat("x", 20000)), 7)
	if len(message) > triggerCorrectionReserve || !strings.Contains(message, "selection_count=7") || !strings.Contains(message, "IGNORE必须空数组") {
		t.Fatal("unbounded or weakened correction", len(message))
	}
	changes := make([]model.ControlChange, 7)
	for i := range changes {
		changes[i].ID = fmt.Sprintf("change-%d", i)
	}
	for _, raw := range []string{`{"decision":"IGNORE","reason":"invalid","relevant_indices":[1]}`, `{"decision":"TRIGGER","reason":"invalid","relevant_indices":[]}`, `{"decision":"UNCERTAIN","reason":"invalid","relevant_indices":[]}`} {
		if _, err := validateTriggerIndices([]byte(raw), changes); err == nil {
			t.Fatal("decision/list semantics relaxed", raw)
		}
	}
}

func TestTriggerOutputNormalizesOrder(t *testing.T) {
	out, err := validateTrigger([]byte(`{"decision":"TRIGGER","reason":"valid","relevant_change_ids":["b","a"]}`), []model.ControlChange{{ID: "a"}, {ID: "b"}})
	if err != nil || !reflect.DeepEqual(out.IDs, []string{"a", "b"}) {
		t.Fatal("order not normalized", out, err)
	}
}
