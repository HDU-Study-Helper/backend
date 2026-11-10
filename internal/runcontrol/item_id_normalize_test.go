package runcontrol

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"testing"
)

func TestSingleRootTypoNormalizesOnlyConsistentRoutes(t *testing.T) {
	raw := `{"items":[{"item_id":"root5","parent_item_id":null,"status":"COMPLETED","reason":"已核查","routes":[{"item_id":"root5","target":"WORKER","workflow_id":"wf","execution":{"run_id":"real"}}]}]}`
	items, e := parseItemResults(raw, "root")
	if e != nil || len(items) != 1 || items[0].ID != "root" || items[0].ReportedID != "root5" || !items[0].IDNormalized || items[0].Routes[0].ItemID != "root" {
		t.Fatal(items, e)
	}
	for _, bad := range []string{`{"items":[{"item_id":"root5","status":"COMPLETED","reason":"x","routes":[]}]}`, `{"items":[{"item_id":"root5","status":"COMPLETED","reason":"x","routes":[{"item_id":"other","target":"WORKER"}]}]}`, `{"items":[{"item_id":"other:1","parent_item_id":"other","status":"COMPLETED","reason":"x"},{"item_id":"other:2","parent_item_id":"other","status":"COMPLETED","reason":"x"}]}`} {
		if _, e = parseItemResults(bad, "root"); e == nil {
			t.Fatal("unsafe root normalize", bad)
		}
	}
}
func TestTypedChangeIDFieldStrictlyMatchesInput(t *testing.T) {
	s := &Service{}
	change := model.ControlChange{ID: "change", RecordID: "record"}
	if !s.validateItemEvidence(context.Background(), "owner", json.RawMessage(`[{"type":"change_id","change_id":"change"}]`), change) {
		t.Fatal("typed change rejected")
	}
	if s.validateItemEvidence(context.Background(), "owner", json.RawMessage(`[{"type":"change_id","change_id":"unknown"}]`), change) {
		t.Fatal("unknown typed change accepted")
	}
}
