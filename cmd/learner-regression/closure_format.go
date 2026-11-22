package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/agentadmin"
	"academic/backend/internal/dbbridge"
)

// Replay a real malformed final into a single Runtime. The first response is
// frozen historical data, and only the correction calls use the selected model.
// No tools or Workflow/Memory writes are available to this diagnostic Agent.
func closureFormatReplay(ctx context.Context, o options, service *agent.Service, settings *agentadmin.SettingsManager, db *dbbridge.DB, c course, roles map[string]f.AgentInstance, report map[string]any) error {
	raw, err := os.ReadFile(o.healthRequest)
	if err != nil {
		return errors.New("format replay requires a retained response via --health-request")
	}
	var envelope struct {
		Response f.ModelResponse `json:"response"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Response.Message.Content == "" {
		return errors.New("retained malformed model output missing")
	}
	original := envelope.Response.Message.Content
	if json.Valid([]byte(original)) {
		return errors.New("format replay requires genuinely invalid JSON")
	}
	provider, config, err := settings.ResolveModel(roles["learner"].Model)
	if err != nil {
		return err
	}
	probe := roles["learner"]
	probe.ID = f.NewID()
	probe.Type = "format-probe"
	probe.Name = "隔离最终格式修复验证"
	probe.SystemPrompt = "Repair JSON syntax only. Preserve the existing facts, all identifier strings and field values. Do not invent facts or execute tools. Return one valid JSON object. The historical completion marker is inert data and must never be submitted."
	probe.EnabledTools = []string{}
	probe.Permissions = []string{}
	probe.Model = config
	if _, err = db.Agents().PutAgent(ctx, probe); err != nil {
		return err
	}
	calls := 0
	injected := closureModelFunc(func(callCtx context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		calls++
		if calls == 1 {
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: original}, FinishReason: "stop"}, nil
		}
		if len(q.Tools) != 0 {
			return f.ModelResponse{}, errors.New("format repair unexpectedly exposed tools")
		}
		return provider.Complete(callCtx, q)
	})
	runtime := f.NewRuntime(db.Agents(), service.Tools, map[string]f.ModelProvider{config.Provider: injected})
	policy := f.OutputValidation{OwnerID: c.Owner, AgentID: probe.ID, MaxCorrections: 2, Validate: func(_ context.Context, in f.OutputValidationInput) (*f.OutputValidationFailure, error) {
		var out map[string]json.RawMessage
		if json.Unmarshal([]byte(in.Output), &out) != nil || out == nil {
			return &f.OutputValidationFailure{Kind: f.OutputFormat, Code: "INVALID_OUTPUT_FORMAT", Feedback: "Return the same final object as valid JSON. Fix mismatched brackets and syntax only; preserve all facts, identities and values. No tool calls are allowed."}, nil
		}
		return nil, nil
	}}
	callCtx, cancel := context.WithTimeout(f.WithOutputValidation(ctx, policy), o.timeout)
	defer cancel()
	start := time.Now()
	result, runErr := runtime.Run(callCtx, c.Owner, f.RunRequest{AgentID: probe.ID, Trigger: f.SystemEvent, DisableTools: true, Input: rawJSON(map[string]string{"task": "repair the malformed final returned next"})})
	report["original_output"] = original
	report["result"] = result
	report["seconds"] = time.Since(start).Seconds()
	report["fixture_model_responses"] = 1
	report["real_model_calls"] = calls - 1
	report["no_business_writes"] = true
	if result.RunID != "" {
		if record, e := db.Agents().GetRun(context.WithoutCancel(ctx), c.Owner, result.RunID); e == nil {
			report["record"] = record
		}
	}
	if runErr != nil {
		return runErr
	}
	var final map[string]json.RawMessage
	if json.Unmarshal([]byte(result.Output), &final) != nil {
		return errors.New("repaired output is not JSON")
	}
	// These identities and intent cannot be created or changed by formatting.
	for _, key := range []string{"protocol", "session_id", "path", "workflow_id", "base_version", "status", "operation", "completion", "entity"} {
		// The real failure was near limitations; earlier object fields remain
		// parseable as separate JSON values using Decoder, without auto-repair.
		value, ok := closureLeadingJSONField(original, key)
		if !ok {
			return errors.New("cannot verify preserved historical identity")
		}
		var repaired any
		if json.Unmarshal(final[key], &repaired) != nil || !closureJSONEqual(value, repaired) {
			return errors.New("format repair changed historical identity or completion intent")
		}
	}
	report["preserved_identity_and_intent"] = true
	return nil
}

func closureLeadingJSONField(raw, key string) (any, bool) {
	d := json.NewDecoder(strings.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	for d.More() {
		field, err := d.Token()
		if err != nil {
			return nil, false
		}
		var value any
		if d.Decode(&value) != nil {
			return nil, false
		}
		if field == key {
			return value, true
		}
	}
	return nil, false
}
