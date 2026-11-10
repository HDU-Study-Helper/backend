package runcontrol

import (
	"academic/backend/internal/model"
	"testing"
)

func TestNormalizeOnlyUniqueAuthoritativeChild(t *testing.T) {
	actual := map[string]model.ControlRun{"real": {ID: "real", OwnerID: "owner", ParentID: "parent", WorkflowID: "wf"}}
	roles := map[string]string{"real": "worker"}
	route := itemRoute{ItemID: "item", Target: "WORKER", WorkflowID: "wf", Execution: &routeExecution{RunID: "typo"}}
	fixed := normalizeRouteRun(route, "item", "owner", "parent", actual, roles, map[string]bool{})
	if fixed.Execution.RunID != "real" || fixed.Execution.ReportedRunID != "typo" || !fixed.Execution.Normalized {
		t.Fatal(fixed)
	}
	omitted := route
	omitted.Execution = nil
	fixed = normalizeRouteRun(omitted, "item", "owner", "parent", actual, roles, map[string]bool{})
	if fixed.Execution == nil || fixed.Execution.RunID != "real" {
		t.Fatal(fixed)
	}
	for _, used := range []map[string]bool{{"real": true}} {
		if v := normalizeRouteRun(route, "item", "owner", "parent", actual, roles, used); v.Execution.RunID != "typo" {
			t.Fatal("used run rebound")
		}
	}
	failed := route
	failed.Execution = &routeExecution{Error: &routeError{Code: "INVALID_ARGUMENT"}}
	if v := normalizeRouteRun(failed, "item", "owner", "parent", actual, roles, nil); v.Execution.RunID != "" {
		t.Fatal("failed preflight rewritten")
	}
	actual["other"] = model.ControlRun{ID: "other", OwnerID: "owner", ParentID: "parent", WorkflowID: "wf"}
	roles["other"] = "worker"
	if v := normalizeRouteRun(route, "item", "owner", "parent", actual, roles, nil); v.Execution.RunID != "typo" {
		t.Fatal("ambiguous match guessed")
	}
	delete(actual, "other")
	if v := normalizeRouteRun(route, "item", "different-owner", "parent", actual, roles, nil); v.Execution.RunID != "typo" {
		t.Fatal("foreign owner matched")
	}
}
