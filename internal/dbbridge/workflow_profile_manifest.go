package dbbridge

import (
	f "academic/agent/framework"
	"encoding/json"
)

// Profile metadata describes the Learner's investigation; it is not a host-side
// completeness gate. Workflow/entity validity and atomic seed normalization,
// ownership checks and persistence receipts are enforced by their existing paths.
func validateInitialCourseProfile(w f.Workflow, _ []f.Memory) error {
	var definition map[string]json.RawMessage
	if err := json.Unmarshal(w.Definition, &definition); err != nil || definition == nil {
		return f.Fail("INVALID_ARGUMENT", "definition must be a JSON object")
	}
	raw, supplied := definition["profile_manifest"]
	if !supplied || string(raw) == "null" {
		return nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return f.Fail("INVALID_ARGUMENT", "definition.profile_manifest must be an object or null when supplied")
	}
	return nil
}
