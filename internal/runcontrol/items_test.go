package runcontrol

import (
	"academic/backend/internal/model"
	"encoding/json"
	"testing"
)

func TestItemCoverageAndMultipleCourseRoutes(t *testing.T) {
	raw := `{"items":[{"item_id":"root:1","parent_item_id":"root","title":"高数作业","status":"COMPLETED","reason":"已处理","routes":[{"item_id":"root:1","target":"WORKER","workflow_id":"wf-math","execution":{"run_id":"r1"}}]},{"item_id":"root:2","parent_item_id":"root","title":"英语考试","status":"COMPLETED","reason":"已处理","routes":[{"item_id":"root:2","target":"WORKER","workflow_id":"wf-english","execution":{"run_id":"r2"}}]},{"item_id":"root:3","parent_item_id":"root","title":"高数补充要求","status":"COMPLETED","reason":"已处理","routes":[{"item_id":"root:3","target":"WORKER","workflow_id":"wf-math","execution":{"run_id":"r3"}}]}]}`
	items, e := parseItemResults(raw, "root")
	if e != nil || len(items) != 3 {
		t.Fatal(items, e)
	}
	for _, bad := range []string{`{"items":[]}`, `{"status":"ROUTED"}`, `{"items":[{"item_id":"other","status":"COMPLETED","reason":"x"}]}`, `{"items":[{"item_id":"root","status":"COMPLETED","reason":"x"},{"item_id":"root:1","parent_item_id":"root","status":"COMPLETED","reason":"x"}]}`} {
		if _, e := parseItemResults(bad, "root"); e == nil {
			t.Fatal("coverage accepted", bad)
		}
	}
}
func TestIgnoredItemAndStableIdentity(t *testing.T) {
	items, e := parseItemResults(`{"items":[{"item_id":"root","title":"普通目录","status":"IGNORED","reason":"首次观察的无行动要求目录","routes":[],"artifacts":[]}]}`, "root")
	if e != nil || items[0].Status != "IGNORED" {
		t.Fatal(items, e)
	}
	a := newControlItem(model.ControlBatch{ID: "b"}, model.ControlChange{ID: "c", Resource: "material"})
	b := newControlItem(model.ControlBatch{ID: "b"}, model.ControlChange{ID: "c"})
	if a.ID != b.ID || a.Status != "PENDING" {
		t.Fatal(a, b)
	}
}
func TestZeroObservedTimeIsNotSourceError(t *testing.T) {
	raw := cleanEvidenceTime(json.RawMessage(`{"title":"通知","observed_at":"0001-01-01T00:00:00Z"}`))
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		t.Fatal(string(raw))
	}
	if _, exists := object["observed_at"]; exists || object["title"] != "通知" {
		t.Fatal(object)
	}
}
func TestRecoveredPreflightFailureRequiresActualSuccessfulRun(t *testing.T) {
	var routes []itemRoute
	_ = json.Unmarshal([]byte(`[{"target":"WORKER","workflow_id":"wf","execution":{"run_id":null,"error":{"code":"INVALID_ARGUMENT"}}},{"target":"WORKER","workflow_id":"wf","execution":{"run_id":"real","error":null}}]`), &routes)
	actual := map[string]model.ControlRun{"real": {ID: "real", State: "succeeded"}}
	if !recoveredPreflightFailure(routes[0], routes, actual) {
		t.Fatal("recovered schema error rejected")
	}
	delete(actual, "real")
	if recoveredPreflightFailure(routes[0], routes, actual) {
		t.Fatal("fabricated recovery accepted")
	}
}
func TestRootItemSelfParentNormalizesToEmpty(t *testing.T) {
	items, e := parseItemResults(`{"items":[{"item_id":"root","parent_item_id":"root","status":"IGNORED","reason":"无行动要求"}]}`, "root")
	if e != nil || items[0].ParentID != "" {
		t.Fatal(items, e)
	}
}
func TestEvidenceReferencesRejectFabricatedSuffix(t *testing.T) {
	change := model.ControlChange{ID: "change", RecordID: "48413", After: json.RawMessage(`{"external_id":"native"}`)}
	for _, raw := range []string{`["change","48413","native"]`, `[{"kind":"change_id","value":"change"}]`} {
		if !validEvidence(json.RawMessage(raw), change) {
			t.Fatal(raw)
		}
	}
	if validEvidence(json.RawMessage(`["change2"]`), change) {
		t.Fatal("fabricated ref accepted")
	}
}
