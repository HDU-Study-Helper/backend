package runcontrol

import (
	f "academic/agent/framework"
	"context"
	"encoding/json"
)

// Optional profile metadata is checked only for JSON shape. Investigation scope,
// coverage and memory topics are the Learner's decisions, not handoff gates.
// verifyLearnerResult separately reads the persisted Workflow and checks its
// owner, active state, entity and version. Atomic memory creation owns its ACLs
// and transaction receipt; no profile category or minimum count is required here.
func (s *Service) verifyWorkflowProfile(_ context.Context, _ string, w f.Workflow) error {
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(w.Definition, &definition); err != nil || definition == nil {
		return f.Fail("INVALID_WORKFLOW_PROFILE", "Workflow definition must be a JSON object")
	}
	raw, supplied := definition["profile_manifest"]
	if !supplied || string(raw) == "null" {
		return nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return f.Fail("INVALID_WORKFLOW_PROFILE", "Workflow profile_manifest must be an object or null when supplied")
	}
	return nil
}
