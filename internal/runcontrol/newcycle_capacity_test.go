package runcontrol

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"os"
	"testing"
)

// Explicit offline preflight of captured batches; never invokes a model or source.
func TestNewcycleCapturedBatchInputCapacity(t *testing.T) {
	path := os.Getenv("NEWCYCLE_CAPTURED_BATCHES")
	if path == "" {
		t.Skip("optional private offline batch capture")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("capture unavailable")
	}
	var batches []model.ControlBatch
	if json.Unmarshal(raw, &batches) != nil {
		t.Fatal("capture invalid")
	}
	groups, singletons, sources, maxBytes := 0, 0, 0, 0
	for _, b := range batches {
		svc, _, store, _, _ := newItemsIntegration(t, "")
		ids := []string{}
		for _, c := range b.Changes {
			ids = append(ids, c.ID)
		}
		b.Trigger = mustJSON(map[string]any{"decision": "TRIGGER", "reason": "offline worst-case all-selected capacity check", "relevant_change_ids": ids})
		units, e := svc.itemWorkUnits(context.Background(), b, b.Changes, store)
		if e != nil {
			t.Fatal("planning failed", e)
		}
		seen := map[string]bool{}
		for _, u := range units {
			encoded, e := buildWorkItemInput(b, u)
			if e != nil {
				t.Fatal("encode", e)
			}
			if len(encoded) > maxBytes {
				maxBytes = len(encoded)
			}
			if u.grouped() {
				groups++
				if len(encoded) > courseGroupInputBytes || len(u.changes) > courseGroupEvents {
					t.Fatal("group bound exceeded")
				}
			} else {
				singletons++
				if len(encoded) > 256<<10 {
					t.Fatal("single source input exceeds reserved initial context budget")
				}
			}
			for _, c := range u.changes {
				if seen[c.ID] {
					t.Fatal("duplicate source")
				}
				seen[c.ID] = true
				sources++
			}
		}
		if len(seen) != len(b.Changes) {
			t.Fatal("lost original evidence")
		}
	}
	if len(batches) != 44 || sources != 3872 {
		t.Fatalf("capture changed: batches=%d source=%d", len(batches), sources)
	}
	summary := map[string]any{"batches": len(batches), "source_changes": sources, "worst_case_filter_groups": groups, "worst_case_singletons": singletons, "max_encoded_input_bytes": maxBytes, "all_sources_preserved": true, "model_requests": 0, "source_requests": 0}
	data, _ := json.MarshalIndent(summary, "", "  ")
	if e = os.WriteFile("../../../.build/newcycle-capacity-preflight.json", data, 0600); e != nil {
		t.Fatal("report save")
	}
	t.Log(string(data))
}
