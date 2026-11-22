package agent

import (
	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
	"context"
	"encoding/json"
)

// RegisterLearnerStageTools registers the two session tools from the same specs
// used by production staging and trusted host-side catalog generation. Registration
// never reads the repository or executes a session operation.
func RegisterLearnerStageTools(tools *f.ToolRegistry, session *stage.Session) error {
	if tools == nil || session == nil {
		return f.Fail("INVALID_ARGUMENT", "staging tool registration requires registry and session")
	}
	object := scheduleObject(map[string]any{})
	stateSchema := learnerStageStateSchema()
	if err := tools.Register(f.ToolSpec{Name: "learner_stage_state", Description: "Read trusted session_id, selected workflow_id, base_version and staging_revision. Read this after edits and use the exact revision in the final READY_TO_COMMIT marker. Staged data is not committed.", InputSchema: object, OutputSchema: stateSchema, ReadOnly: true}, func(context.Context, f.ExecutionContext, json.RawMessage) (any, error) { return session.State(), nil }); err != nil {
		return err
	}
	return tools.Register(f.ToolSpec{Name: "learner_stage_select", Description: "Select exactly one owned Workflow for this Learner staging session; all of its private memories become readable/editable in this session. Revision calls cannot switch away from the host-authorized target. For initialization use workflow_create to create a staged target, or select an existing candidate.", InputSchema: scheduleObject(map[string]any{"workflow_id": scheduleText(128, "Exact owned Workflow ID from workflow_list/get or trusted staging state. Selects one session target; REVISE cannot switch away from its host-authorized target.")}, "workflow_id"), OutputSchema: stateSchema, Permissions: []string{"workflow.read"}}, func(ctx context.Context, _ f.ExecutionContext, raw json.RawMessage) (any, error) {
		var args struct {
			ID string `json:"workflow_id"`
		}
		_ = json.Unmarshal(raw, &args)
		if err := session.Select(ctx, args.ID); err != nil {
			return nil, err
		}
		return session.State(), nil
	})
}

func learnerStageStateSchema() json.RawMessage {
	return scheduleObject(map[string]any{
		"protocol":            map[string]any{"type": "string", "const": stage.Protocol, "description": "Host staging protocol, not a model-selected format."},
		"session_id":          scheduleText(128, "Exact server-generated staging session ID; use unchanged in completion marker."),
		"path":                scheduleEnum("Host-authorized path; model input cannot change it.", "INITIALIZE", "REVISE"),
		"workflow_id":         map[string]any{"type": "string", "maxLength": 128, "description": "Selected staged Workflow ID, or empty before selection/create. Does not grant unrelated Workflow access."},
		"workflow_status":     map[string]any{"type": "string", "enum": []string{"", "draft", "active", "archived"}, "description": "Current staged status; empty before selection/create. Initialization must explicitly reach active."},
		"memory_count":        map[string]any{"type": "integer", "minimum": 0, "description": "Count of target Workflow memories in the staging overlay; not a committed write receipt."},
		"base_version":        map[string]any{"type": []string{"integer", "null"}, "minimum": 1, "description": "Existing Workflow baseline version, or null for a new staged Workflow."},
		"staging_revision":    map[string]any{"type": "integer", "minimum": 0, "description": "Exact revision after staged edits. Re-read and use unchanged in final READY_TO_COMMIT marker."},
		"staged":              map[string]any{"type": "boolean", "const": true, "description": "This result describes an overlay only."},
		"committed":           map[string]any{"type": "boolean", "const": false, "description": "Always false here; only a host commit receipt proves formal persistence."},
		"completion_blockers": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Host-detected blockers. Empty does not itself commit or validate every business claim."},
	}, "protocol", "session_id", "path", "workflow_id", "workflow_status", "memory_count", "base_version", "staging_revision", "staged", "committed", "completion_blockers")
}

// stagingToolSpec transforms the actual registration contract, rather than only
// the provider's display. Model-visible and executed schemas are the same.
func stagingToolSpec(spec f.ToolSpec) f.ToolSpec {
	if spec.Name != "workflow_create" && spec.Name != "memory_create" && spec.Name != "memory_update" {
		return spec
	}
	var schema map[string]any
	if json.Unmarshal(spec.InputSchema, &schema) != nil {
		return spec
	}
	props, _ := schema["properties"].(map[string]any)
	if spec.Name == "workflow_create" {
		delete(props, "initial_private_memories")
		spec.Description = "Create the selected Workflow in staging only; then use memory_create to edit its private memories. Nothing is committed until the final READY_TO_COMMIT result is host-validated. initial_private_memories is not supported in staging."
	} else {
		if spec.Name == "memory_create" {
			spec.Description = "Create a sourced memory in the selected staging Workflow PRI only. Omitted ownership is bound to that target; no PUB or Agent PRI writes. Changes remain staged until host commit."
		} else {
			spec.Description = "Patch a selected target Workflow's staged private memory. Omitted fields stay unchanged; supplied structured_content/source_refs replace those whole fields, not merge or append. Source references must remain nonempty; no PUB/Agent PRI or other Workflow writes. No formal write until host commit."
		}
		props["visibility"] = map[string]any{"type": "string", "const": "PRI", "description": "Staging target memories are private to the selected Workflow; PUB is not writable."}
		props["owner_agent_id"] = map[string]any{"type": "null", "description": "Must be omitted or null; Agent-private ownership is not writable in this session."}
		props["owner_workflow_id"] = map[string]any{"type": []string{"string", "null"}, "maxLength": 128, "description": "Omit to use the selected Workflow, or supply its exact trusted ID. Another Workflow is rejected."}
		props["source_refs"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "description": "Nonempty retained source references for the staged fact. Supplying this replaces the full field; preserve still-valid references. Values retain the existing legacy-reference compatibility."}
		if spec.Name == "memory_create" {
			required, _ := schema["required"].([]any)
			required = append(required, "source_refs")
			schema["required"] = required
		}
	}
	spec.InputSchema = mustStageJSON(schema)
	return spec
}
