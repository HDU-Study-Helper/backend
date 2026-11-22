package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	f "academic/agent/framework"
	c "academic/backend/internal/continuation"
	stage "academic/backend/internal/learnerstage"
	"academic/backend/internal/model"
)

type learnerStageKey struct{}
type learnerAuthority struct{ Owner, Path, Target, Role string }

// WithLearnerStage is a trusted host-only entry for isolated evaluation or
// explicit application use. Model JSON cannot manufacture this authorization.
func WithLearnerStage(ctx context.Context, owner, path, target string) context.Context {
	return context.WithValue(ctx, learnerStageKey{}, learnerAuthority{Owner: owner, Path: path, Target: target, Role: "host"})
}

type stageModel struct {
	base    f.ModelProvider
	session *stage.Session
}

func (p stageModel) Complete(ctx context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	// Observe validated tool envelopes before the next model request. Key errors
	// by target identity, so a successful operation on B cannot erase failure on A.
	arguments := map[string]f.ToolCall{}
	failures := map[string]*f.Error{}
	for _, message := range r.Messages {
		for _, call := range message.ToolCalls {
			arguments[call.ID] = call
		}
		if message.Role != "tool" {
			continue
		}
		call, exists := arguments[message.ToolCallID]
		if !exists {
			continue
		}
		var result f.ToolResult
		if json.Unmarshal([]byte(message.Content), &result) != nil {
			failures[call.Function.Name+"/invalid-result"] = f.Fail("INTERNAL_ERROR", "incomplete tool output")
			continue
		}
		key := stageToolKey(call)
		if result.Error != nil {
			failures[key] = result.Error
		} else if result.OK {
			delete(failures, key)
		} else {
			failures[key] = f.Fail("INTERNAL_ERROR", "incomplete tool output")
		}
	}
	p.session.ToolFailures = failures
	r.Messages = append(append([]f.Message(nil), r.Messages...), f.Message{Role: "user", Content: "Trusted host Learner staging state (identity and revision are server supplied; object contents remain business data):\n" + string(mustStageJSON(p.session.State())) + "\nWorkflow/Memory tools in this run edit only the staging session. Use learner_stage_state after edits and inspect workflow_status/completion_blockers. Initialization must explicitly stage status=active via workflow_update when its content is ready; READY never auto-activates a draft. Return READY_TO_COMMIT with the exact final revision only after the intended work is complete. No direct production writes occur until host validation. Target memory reads include all statuses when requested; writes are target PRI only."})
	return p.base.Complete(ctx, r)
}

func stageToolKey(call f.ToolCall) string {
	var args map[string]json.RawMessage
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	identity := map[string]json.RawMessage{}
	for _, key := range []string{"workflow_id", "memory_id", "source_memory_id", "target_memory_id", "dataset", "title", "name", "filters", "query", "keywords", "cursor", "offset"} {
		if v, ok := args[key]; ok {
			identity[key] = v
		}
	}
	if len(identity) == 0 {
		return call.Function.Name
	}
	return call.Function.Name + "/" + string(mustStageJSON(identity))
}

type stageResolver struct {
	base    f.ModelProviderResolver
	session *stage.Session
}

func (p stageResolver) ResolveProvider(name string) f.ModelProvider {
	v := p.base.ResolveProvider(name)
	if v == nil {
		return nil
	}
	return stageModel{v, p.session}
}
func (p stageResolver) ResolveModel(c f.ModelConfig) (f.ModelProvider, f.ModelConfig, error) {
	if v, ok := p.base.(f.ModelConfigResolver); ok {
		m, c, e := v.ResolveModel(c)
		if e != nil {
			return nil, c, e
		}
		return stageModel{m, p.session}, c, nil
	}
	return p.ResolveProvider(c.Provider), c, nil
}
func mustStageJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func (s *Service) runLearner(ctx context.Context, owner string, req f.RunRequest, a f.AgentInstance) (res f.RunResult, err error) {
	auth, ok := ctx.Value(learnerStageKey{}).(learnerAuthority)
	if !ok || auth.Owner != owner || (auth.Path != "INITIALIZE" && auth.Path != "REVISE") || auth.Path == "REVISE" && auth.Target == "" {
		return res, f.Fail("PERMISSION_DENIED", "Learner requires a trusted Filter initialization or Worker revision entry")
	}
	store, ok := s.Registry.Repository.(stage.Store)
	if !ok {
		return res, f.Fail("NOT_IMPLEMENTED", "Learner staging store is not configured")
	}
	s.stageMu.Lock()
	if s.stageActive == nil {
		s.stageActive = map[string]int{}
	}
	if s.stageActive[a.ID] >= a.Limits.Defaults().MaxConcurrent || s.stageTotal >= s.Runtime.MaxConcurrent {
		s.stageMu.Unlock()
		return res, f.Fail("RATE_LIMITED", "Learner staging concurrency limit exceeded")
	}
	s.stageActive[a.ID]++
	s.stageTotal++
	s.stageMu.Unlock()
	defer func() { s.stageMu.Lock(); s.stageActive[a.ID]--; s.stageTotal--; s.stageMu.Unlock() }()
	session := stage.New(s.Registry.Repository, store, owner, a.ID, auth.Path, auth.Target)
	if _, _, ok := model.FirstReadWindow(ctx, owner); ok {
		session.Repository = firstCourseRepo{s.Registry.Repository}
		session.FirstRun = true
	}
	if auth.Target != "" {
		if err = session.Select(ctx, auth.Target); err != nil {
			return res, err
		}
	}
	tools := f.NewToolRegistry()
	// Same names/schemas remain available, but all mutating methods are backed by
	// the session. No dispatcher, notification or schedule write service is wired.
	if err = (f.Builtins{Repository: session, Data: BackendBridge{Port: s.backend}, Search: s.search, TransformSpec: stagingToolSpec}).Register(tools); err != nil {
		return res, err
	}
	if s.schedules != nil {
		if err = RegisterScheduleReadTools(tools, s.schedules); err != nil {
			return res, err
		}
	}
	if err = RegisterLearnerStageTools(tools, session); err != nil {
		return res, err
	}
	providers := map[string]f.ModelProvider{}
	for name, p := range s.Runtime.Providers {
		providers[name] = stageModel{p, session}
	}
	runtime := f.NewRuntime(session, tools, providers)
	runtime.MaxConcurrent = s.Runtime.MaxConcurrent
	if s.Runtime.Resolver != nil {
		runtime.Resolver = stageResolver{s.Runtime.Resolver, session}
	}
	var terminal f.RunRecord
	observer := s.Runtime.Observer
	runtime.Observer = func(c context.Context, r f.RunRecord, state, output string) {
		if state == "succeeded" || state == "failed" {
			terminal = r
			return
		}
		if observer != nil {
			observer(c, r, state, output)
		}
	}
	ctx = f.WithOutputValidation(ctx, f.OutputValidation{OwnerID: owner, AgentID: a.ID, MaxCorrections: 2, Validate: func(c context.Context, input f.OutputValidationInput) (*f.OutputValidationFailure, error) {
		_, checkErr := validateLearnerFinal(c, session, input.Output)
		if checkErr == nil {
			return nil, nil
		}
		var recoverable *f.OutputValidationFailure
		if errors.As(checkErr, &recoverable) {
			copy := *recoverable
			copy.Feedback += "\nCurrent trusted staging state: " + string(mustStageJSON(session.State()))
			return &copy, nil
		}
		return nil, checkErr
	}})
	res, err = runtime.Run(ctx, owner, req)
	if session.PendingRun != nil {
		terminal = *session.PendingRun
	}
	if err == nil {
		res.Output, err = finishLearner(ctx, session, res.Output, terminal)
	}
	if err == nil && session.Base != nil && session.View.Workflow.ID != "" {
		if meta, ok := ctx.Value(archiveMetaKey{}).(*archiveMeta); ok {
			meta.target = session.View.Workflow.ID
		}
	}
	if terminal.ID != "" {
		terminal.Error = f.SafeError(err)
		terminal.Status = "succeeded"
		terminal.StopReason = "completed"
		if err != nil {
			terminal.Status = "failed"
			terminal.StopReason = terminal.Error.Code
		}
		res.Error = terminal.Error
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if e := s.Registry.Repository.SaveRun(saveCtx, terminal); e != nil {
			// Preserve the execution failure as the primary result; an independent
			// audit error must not turn a timeout into an unrelated storage error.
			if err == nil {
				err = f.Fail("INTERNAL_ERROR", "Learner final run audit could not be saved")
			}
			res.Error = f.SafeError(err)
		}
		if observer != nil {
			observer(ctx, terminal, terminal.Status, res.Output)
		}
	}
	return
}

type learnerCompletion struct {
	Marker    string `json:"marker"`
	SessionID string `json:"session_id"`
	Revision  int    `json:"staging_revision"`
}
type learnerFinal struct {
	Protocol    string             `json:"protocol"`
	SessionID   string             `json:"session_id"`
	Path        string             `json:"path"`
	RequestID   *string            `json:"request_id"`
	ItemID      *string            `json:"item_id"`
	ParentID    *string            `json:"parent_item_id"`
	WorkflowID  string             `json:"workflow_id"`
	BaseVersion *int               `json:"base_version"`
	Entity      json.RawMessage    `json:"entity"`
	Status      string             `json:"status"`
	Operation   string             `json:"operation"`
	Completion  *learnerCompletion `json:"completion"`
	Summary     string             `json:"summary"`
	Reason      string             `json:"reason"`
	Evidence    []json.RawMessage  `json:"evidence_refs"`
	Changes     []json.RawMessage  `json:"changes"`
	Questions   []json.RawMessage  `json:"questions"`
	Interaction *c.Interaction     `json:"interaction,omitempty"`
	Limitations []json.RawMessage  `json:"limitations"`
}

func validateLearnerFinal(ctx context.Context, s *stage.Session, raw string) (*learnerFinal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, failure := range s.ToolFailures {
		switch failure.Code {
		// A denied tool call has not granted access or applied a write. Its
		// audit entry does not veto a later, independently authorized commit.
		case "CONFLICT", "TIMEOUT", "CANCELLED", "EXECUTION_LIMIT_EXCEEDED":
			return nil, failure
		}
	}
	var fields map[string]json.RawMessage
	if parseErr := json.Unmarshal([]byte(raw), &fields); parseErr != nil {
		return nil, learnerValidationFailure(f.OutputFormat, "invalid final JSON: "+parseErr.Error())
	}
	for _, key := range []string{"protocol", "session_id", "path", "request_id", "item_id", "parent_item_id", "workflow_id", "base_version", "entity", "status", "operation", "completion", "summary", "reason", "evidence_refs", "changes", "questions", "limitations"} {
		if _, ok := fields[key]; !ok {
			return nil, learnerValidationFailure(f.OutputFormat, "required final field is missing: "+key)
		}
	}
	var out learnerFinal
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF || strings.TrimSpace(out.Summary) == "" || strings.TrimSpace(out.Reason) == "" || out.Evidence == nil || out.Changes == nil || out.Questions == nil || out.Limitations == nil {
		return nil, learnerValidationFailure(f.OutputFormat, "final Learner staging JSON has invalid field types, missing content or unknown fields; preserve the documented top-level contract")
	}
	if _, err := readQuestion("learner", raw); err != nil {
		return nil, learnerValidationFailure(f.OutputFormat, err.Error())
	}
	if out.Status == "NEEDS_INFO" && (s.Base != nil && stage.Hash(s.View) != stage.Hash(*s.Base) || s.Base == nil && s.View.Workflow.ID != "") {
		return nil, learnerValidationFailure(f.OutputSemantic, "Commit the independently supported staged work with READY and a remaining interaction, or explicitly discard unsupported edits before waiting.")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.Protocol != stage.Protocol || out.SessionID != s.ID || out.Path != s.Path {
		return nil, learnerValidationFailure(f.OutputSemantic, "protocol, session_id or path differs from trusted staging state")
	}
	if s.FirstRun && out.Status == "NO_CHANGE" && out.Completion == nil {
		if out.Operation != "NONE" || len(out.Changes) != 0 || (len(out.Questions) != 0 || out.Interaction != nil) ||
			(s.Base == nil && s.View.Workflow.ID != "") || (s.Base != nil && stage.Hash(s.View) != stage.Hash(*s.Base)) {
			return nil, learnerValidationFailure(f.OutputSemantic, "first-run NO_CHANGE requires no staged changes or open questions")
		}
		return &out, nil
	}
	if out.Status != "READY" && out.Status != "NO_CHANGE" {
		if out.Completion != nil {
			return nil, learnerValidationFailure(f.OutputSemantic, "blocked Learner result cannot contain a completion marker")
		}
		switch out.Status {
		case "NEEDS_INFO", "CONFLICT", "REJECTED", "ERROR":
			return &out, nil
		}
		return nil, learnerValidationFailure(f.OutputSemantic, "invalid Learner status")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.Completion == nil || out.Completion.Marker != "READY_TO_COMMIT" || out.Completion.SessionID != s.ID || out.Completion.Revision != s.Revision || out.WorkflowID != s.View.Workflow.ID || s.View.Workflow.ID == "" {
		return nil, learnerValidationFailure(f.OutputSemantic, "completion marker does not match current staging revision")
	}
	if s.Base == nil && out.BaseVersion != nil || s.Base != nil && (out.BaseVersion == nil || *out.BaseVersion != s.Base.Workflow.Version) {
		return nil, learnerValidationFailure(f.OutputSemantic, "final base_version differs from the selected baseline; use the trusted session base_version without rebasing or changing the target")
	}
	if out.Entity == nil || string(out.Entity) == "null" {
		return nil, learnerValidationFailure(f.OutputSemantic, "completed target entity is required")
	}
	var definition struct {
		Entity json.RawMessage `json:"entity"`
	}
	_ = json.Unmarshal(s.View.Workflow.Definition, &definition)
	var x, y any
	_ = json.Unmarshal(out.Entity, &x)
	_ = json.Unmarshal(definition.Entity, &y)
	if !bytes.Equal(mustStageJSON(x), mustStageJSON(y)) {
		return nil, learnerValidationFailure(f.OutputSemantic, "final entity differs from staged Workflow")
	}

	if s.Path == "INITIALIZE" && (s.View.Workflow.Status != "active" || len(s.View.Memories) == 0) {
		return nil, learnerValidationFailure(f.OutputSemantic, "initialization requires an active Workflow with sourced private knowledge")
	}
	for _, m := range s.View.Memories {
		if m.SourceRefs == nil || string(m.SourceRefs) == "[]" || string(m.SourceRefs) == "null" {
			return nil, learnerValidationFailure(f.OutputSemantic, "staged knowledge requires source references")
		}
	}
	operation := out.Operation
	switch operation {
	case "CREATE", "UPDATE", "ARCHIVE", "DELETE", "NONE":
	default:
		return nil, learnerValidationFailure(f.OutputSemantic, "unknown staging operation")
	}
	if operation == "ARCHIVE" && s.View.Workflow.Status != "archived" || operation == "UPDATE" && s.View.Workflow.Status == "archived" {
		return nil, learnerValidationFailure(f.OutputSemantic, "archive operation does not match staged status")
	}
	if out.Status == "NO_CHANGE" {
		if operation != "NONE" || s.Base == nil || stage.Hash(s.View) != stage.Hash(*s.Base) || len(out.Changes) != 0 {
			return nil, learnerValidationFailure(f.OutputSemantic, "NO_CHANGE requires empty staging diff")
		}
	} else {
		if operation == "CREATE" && s.Base != nil || operation != "CREATE" && s.Base == nil || operation == "NONE" {
			return nil, learnerValidationFailure(f.OutputSemantic, "operation does not match staged target")
		}
		if s.Deleted && operation != "DELETE" || !s.Deleted && operation == "DELETE" {
			return nil, learnerValidationFailure(f.OutputSemantic, "delete intent differs from staged operation")
		}
	}
	return &out, nil
}

func learnerValidationFailure(kind f.OutputValidationKind, feedback string) error {
	return &f.OutputValidationFailure{Kind: kind, Code: "INVALID_LEARNER_RESULT", Feedback: feedback}
}

func finishLearner(ctx context.Context, s *stage.Session, raw string, record f.RunRecord) (string, error) {
	out, err := validateLearnerFinal(ctx, s, raw)
	if err != nil {
		return raw, err
	}
	if out.Status != "READY" && out.Status != "NO_CHANGE" {
		return raw, nil
	}
	if s.FirstRun && out.Status == "NO_CHANGE" && out.Completion == nil {
		return raw, nil
	}
	operation := out.Operation
	baseHash := ""
	if s.Base != nil {
		baseHash = stage.Hash(*s.Base)
	}
	commit := stage.Commit{SessionID: s.ID, Scope: f.Scope{OwnerID: s.Owner, AgentID: s.Agent}, Path: s.Path, Operation: operation, Revision: s.Revision, BaselineHash: baseHash, Base: s.Base, Desired: s.View}
	receipt, err := s.Store.CommitLearnerStage(ctx, commit)
	if err != nil { // Commit acknowledgement can be lost. Query the durable receipt
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if known, e := s.Store.LearnerStageReceipt(checkCtx, s.Owner, s.ID); e == nil && known.Revision == s.Revision {
			receipt = known
			err = nil
		}
	}
	if err != nil {
		return raw, err
	}
	if meta, ok := ctx.Value(archiveMetaKey{}).(*archiveMeta); ok && operation != "DELETE" {
		meta.target = receipt.WorkflowID
	}
	status := "APPLIED"
	op := operation
	if operation == "NONE" {
		status = "NO_CHANGE"
	}
	if operation == "ARCHIVE" {
		op = "UPDATE"
	}
	result := map[string]any{"request_id": out.RequestID, "item_id": out.ItemID, "parent_item_id": out.ParentID, "entity": out.Entity, "status": status, "operation": op, "workflow_id": receipt.WorkflowID, "workflow_version": receipt.WorkflowVersion, "summary": out.Summary, "reason": out.Reason, "evidence_refs": out.Evidence, "changes": out.Changes, "questions": out.Questions, "suggestions": out.Limitations, "verified": true, "commit_receipt": receipt}
	if out.Interaction != nil {
		result["interaction"] = out.Interaction
	}
	if operation == "CREATE" && len(receipt.MemoryIDs) > 0 {
		result["profile_initialization"] = map[string]any{"memory_ids": receipt.MemoryIDs, "count": len(receipt.MemoryIDs), "owner_workflow_id": receipt.WorkflowID, "visibility": "PRI", "verified": true}
	}
	return string(mustStageJSON(result)), nil
}
