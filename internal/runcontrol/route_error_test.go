package runcontrol

import (
	"strings"
	"testing"
)

func TestFilterQuotedToolErrorsRemainFailedAttempts(t *testing.T) {
	for _, errorValue := range []string{`"INVALID_ARGUMENT: missing required task"`, `"INVALID_ARGUMENT：缺content"`, `{"code":"INVALID_ARGUMENT","message":"missing task"}`} {
		raw := `{"items":[{"item_id":"root","status":"PARTIAL","reason":"工具参数失败，未执行Worker","routes":[{"target":"WORKER","workflow_id":"wf","execution":{"run_id":null,"error":` + errorValue + `}}]}]}`
		items, e := parseItemResults(raw, "root")
		if e != nil || items[0].Routes[0].Execution.Error.Code != "INVALID_ARGUMENT" || items[0].Routes[0].Execution.RunID != "" {
			t.Fatal(items, e)
		}
	}
}
func TestFilterResultTypeErrorNamesField(t *testing.T) {
	_, e := parseItemResults(`{"items":[{"item_id":"root","status":"PARTIAL","reason":"x","routes":"wrong"}]}`, "root")
	if e == nil || !strings.Contains(e.Error(), "routes") {
		t.Fatal(e)
	}
}
