package actions

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

type obj = map[string]any

// Apply shared field documentation consistently to inputs and receipt projections.
var fieldDocs = map[string]string{
	"mode": "Local presentation only: active requires a genuine user instruction; silent does not interrupt.", "evidence": "Source references or short citations supporting this action; not independent verification of a claim.", "job_kind": "Choose a recorded notice, allowlisted tool, or an originating Worker self-run.", "tool": "For ToolCall only: one of the explicitly enumerated targets; current grants are rechecked when due.", "arguments": "Strict target-specific JSON object. No identity, bridge operation, or target-request key.", "limit": "Maximum rows returned, default 20, maximum 100.", "offset": "Zero-based offset in newest-change-first order; advance by number of returned items.", "kind": "Persisted local entity category. Use kind with id for scoped readback.", "status": "Persisted local state; recorded is not external delivery.", "input": "Stored action fields and citations; not authorization to execute a different action.", "updated_at": "Timestamp of the persisted change (RFC3339).", "result": "Bounded actual tool result or receipt from due execution when available.", "result_ref": "Reference to recorded local result, or safe state/failure marker; never a push receipt.", "items": "Current-owner results in descending update-time order; empty array when none."}

func object(props obj, required ...string) obj {
	for k, v := range props {
		if child, ok := v.(map[string]any); ok && child["description"] == nil && fieldDocs[k] != "" {
			copy := maps.Clone(child)
			copy["description"] = fieldDocs[k]
			props[k] = copy
		}
	}
	m := obj{"type": "object", "additionalProperties": false, "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func text(n int, description string) obj {
	return obj{"type": "string", "minLength": 1, "maxLength": n, "description": description}
}
func enum(values ...string) obj { return obj{"type": "string", "enum": values} }
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

var fields = obj{
	"intent_turn_id": text(128, "Active only: raw ID of authenticated owner's Chat turn or answered ChA/ChB question containing the reminder instruction; never quote model text."), "intent_quote": text(1024, "Active only: verbatim user instruction, not a severity estimate. If no instruction use silent."), "preference_memory_id": text(128, "Required for recurring active notices: active personal memory with source_refs containing chat_turn:<turn ID> or question_answer:<ChA/ChB question ID> for the actual user instruction."), "reminder_key": text(160, "Stable owner-scoped identity for this reminder across channels; keep unchanged on retries or deadline changes."),
	"idempotency_key": text(160, "Stable request identifier; reuse only for identical arguments. Use a new key for a new revision."), "todo_key": text(160, "Stable real-world task identity, shared across channels and deadline changes."), "title": text(512, "Concise user-visible title."), "body": text(8192, "Details, notice text, or bounded self-run task."), "due_at": obj{"type": "string", "format": "date-time", "description": "RFC3339 instant with explicit offset; omit unknown todo deadlines."}, "evidence": obj{"type": "array", "minItems": 1, "maxItems": 16, "items": text(512, "Source record reference or short citation; do not invent evidence.")}, "mode": enum("active", "silent"), "reason": text(2048, "Explain why the cited evidence warrants this interruption mode."), "job_kind": enum("Notice", "ToolCall", "Worker"), "tool": enum("notice_list", "public_todo_list", "schedule_get", "public_todo_upsert", "public_todo_complete"), "arguments": object(obj{"id": text(128, "Required for schedule_get or public_todo_complete; for lists use {}."), "todo_key": text(160, "Required for public_todo_upsert."), "title": text(512, "Required for public_todo_upsert."), "body": text(8192, "Optional public_todo_upsert details."), "due_at": obj{"type": "string", "format": "date-time", "description": "Optional todo deadline (not the parent job's due_at)."}}), "repeat_weekly": obj{"type": "boolean", "description": "Repeat at the same local weekday/time, only after successful execution."}, "timezone": text(80, "IANA zone required for weekly recurrence, e.g. Asia/Shanghai."),
}

func input(keys []string, req ...string) obj {
	p := obj{}
	for _, k := range keys {
		p[k] = fields[k]
	}
	return object(p, req...)
}

type definition struct {
	name, kind, perm, desc string
	schema                 obj
	read                   bool
}

func definitions() []definition {
	page := object(obj{"limit": obj{"type": "integer", "minimum": 1, "maximum": 100}, "offset": obj{"type": "integer", "minimum": 0, "maximum": 100000}})
	id := object(obj{"id": text(160, "ID of the persisted current-owner item.")}, "id")
	todo := []string{"idempotency_key", "todo_key", "title", "body", "due_at", "evidence"}
	notice := []string{"idempotency_key", "title", "body", "mode", "reason", "evidence", "due_at", "repeat_weekly", "timezone", "intent_turn_id", "intent_quote", "preference_memory_id", "reminder_key"}
	job := append(slices.Clone(notice), "job_kind", "tool", "arguments")
	return []definition{
		{"public_todo_upsert", "todo", "todo.write", "Create or revise a persistent current-owner public todo. Keep todo_key stable across channels/changes. Omitted body/deadline and existing completion are preserved.", input(todo, "idempotency_key", "todo_key", "title", "evidence"), false},
		{"public_todo_list", "todo", "todo.read", "List current owner's shared todos; newest changes first. Paginate with limit/offset.", page, true},
		{"public_todo_complete", "todo", "todo.write", "Mark one current-owner public todo complete; repeating has no effect.", id, false},
		{"notice_create", "notice", "notice.write", "Record a local active/silent notice based on cited evidence and model rationale. due_at queues instead; no external push or delivery claim.", input(notice, "idempotency_key", "title", "body", "mode", "reason", "evidence"), false},
		{"notice_list", "notice", "notice.read", "List recorded local notices, not delivery receipts.", page, true},
		{"planned_job_create", "job", "job.write", "Schedule one Notice (title/body/mode/reason), local ToolCall (allowlisted tool/arguments) or Worker self-run (body). Weekly recurrence needs timezone. Target grants are checked again when due.", input(job, "idempotency_key", "job_kind", "due_at", "evidence"), false},
		{"planned_job_list", "job", "job.read", "List persisted planned jobs, due dates, states and actual result references.", page, true},
		{"planned_job_cancel", "job", "job.write", "Cancel a pending job. Running jobs cannot be cancelled here.", id, false},
	}
}
func RegisterTools(reg *f.ToolRegistry, repo model.ActionRepository) error {
	if reg == nil || repo == nil {
		return model.ErrInvalidArgument
	}
	item := object(obj{"id": text(160, "Persisted item ID"), "kind": enum("todo", "notice", "job"), "status": enum("open", "complete", "recorded", "pending", "running", "succeeded", "failed", "cancelled"), "source_agent_id": text(160, "Trusted creator or updating agent"), "source_run_id": text(160, "Trusted originating run"), "input": obj{"type": "object"}, "updated_at": obj{"type": "string"}, "result_ref": obj{"type": "string"}, "result": obj{"type": "object"}}, "id", "kind", "status", "input", "source_run_id", "source_agent_id", "updated_at")
	for _, d := range definitions() {
		out := item
		if d.read {
			out = object(obj{"items": obj{"type": "array", "items": item}}, "items")
		}
		err := reg.Register(f.ToolSpec{Name: d.name, Permissions: []string{d.perm}, Description: d.desc, InputSchema: raw(d.schema), OutputSchema: raw(out), ReadOnly: d.read}, func(ctx context.Context, e f.ExecutionContext, b json.RawMessage) (any, error) {
			v, err := handle(ctx, repo, d, e, b)
			switch {
			case errors.Is(err, model.ErrInvalidArgument):
				return nil, f.Fail("INVALID_ARGUMENT", "invalid action fields, evidence, time, or target combination")
			case errors.Is(err, model.ErrConflict):
				return nil, f.Fail("CONFLICT", "idempotency key used for different arguments")
			case errors.Is(err, model.ErrNotFound):
				return nil, f.Fail("RESOURCE_NOT_FOUND", "action or actor not found in current scope/state")
			}
			return v, err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func handle(ctx context.Context, r model.ActionRepository, d definition, e f.ExecutionContext, b json.RawMessage) (any, error) {
	s := model.ActionScope{OwnerID: e.Scope.OwnerID, AgentID: e.Scope.AgentID, RunID: e.RunID}
	if s.OwnerID == "" || s.AgentID == "" || s.RunID == "" || !e.Has(d.perm) || !slices.Contains(e.EnabledTools, d.name) {
		return nil, f.Fail("PERMISSION_DENIED", "trusted runtime grant required")
	}
	if d.read {
		var p struct{ Limit, Offset int }
		if json.Unmarshal(b, &p) != nil {
			return nil, model.ErrInvalidArgument
		}
		if p.Limit == 0 {
			p.Limit = 20
		}
		items, err := r.ListActions(ctx, s.OwnerID, d.kind, p.Limit, p.Offset)
		return obj{"items": items}, err
	}
	if d.name == "public_todo_complete" || d.name == "planned_job_cancel" {
		var p struct{ ID string }
		if json.Unmarshal(b, &p) != nil {
			return nil, model.ErrInvalidArgument
		}
		return r.ChangeAction(ctx, s, d.kind, p.ID)
	}
	var in model.ActionInput
	if json.Unmarshal(b, &in) != nil {
		return nil, model.ErrInvalidArgument
	}
	kind := d.kind
	in.Origin = d.name
	if kind == "notice" && in.Due != nil {
		kind = "job"
		in.Kind = "Notice"
	}
	if err := in.Validate(kind); err != nil {
		return nil, err
	}
	if kind == "job" {
		name, p := "", ""
		switch in.Kind {
		case "ToolCall":
			name = in.Tool
			p = scheduledPermissions[name]
		case "Notice":
			name, p = "notice_create", "notice.write"
		}
		if name != "" && (!e.Has(p) || !slices.Contains(e.EnabledTools, name)) {
			return nil, f.Fail("PERMISSION_DENIED", "target not currently authorized")
		}
	}
	return r.PutAction(ctx, s, kind, in)
}
