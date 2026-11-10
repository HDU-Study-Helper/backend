package runcontrol

import (
	"academic/backend/internal/model"
	"strings"
)

// A copied or omitted run reference can be corrected only when the authority-side
// child set yields exactly one same-owner, same-parent, same-role/workflow run.
// Explicit failed preflight attempts and duplicate actual references remain intact.
func normalizeRouteRun(route itemRoute, itemID, owner, parent string, actual map[string]model.ControlRun, roles map[string]string, used map[string]bool) itemRoute {
	if route.ItemID != "" && route.ItemID != itemID {
		return route
	}
	if route.Execution != nil {
		if _, exists := actual[route.Execution.RunID]; exists {
			return route
		}
		if route.Execution.Error != nil {
			return route
		}
	}
	if route.Target == "WORKER" && route.WorkflowID == "" {
		return route
	}
	candidates := []string{}
	for id, run := range actual {
		if used[id] || run.OwnerID != owner || run.ParentID != parent || !strings.EqualFold(roles[id], route.Target) {
			continue
		}
		if route.Target == "WORKER" && run.WorkflowID != route.WorkflowID {
			continue
		}
		candidates = append(candidates, id)
	}
	if len(candidates) != 1 {
		return route
	}
	reported := ""
	if route.Execution != nil {
		reported = route.Execution.RunID
	}
	route.Execution = &routeExecution{RunID: candidates[0], ReportedRunID: reported, Normalized: true}
	return route
}
