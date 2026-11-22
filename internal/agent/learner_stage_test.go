package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
)

type stageStub struct {
	*repoStub
	snapshot stage.Snapshot
	commits  int
	receipt  stage.Receipt
}

func (r *stageStub) SaveRun(_ context.Context, run f.RunRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, exists := r.runs[run.ID]
	if run.Status == "running" {
		if exists {
			return f.Fail("INVALID_ARGUMENT", "duplicate running audit")
		}
	} else if !exists || old.Status != "running" {
		return f.Fail("RESOURCE_NOT_FOUND", "terminal transition already saved")
	}
	r.runs[run.ID] = run
	return nil
}

func (r *stageStub) SearchMemory(context.Context, f.Scope, f.MemorySearch) ([]f.Memory, error) {
	return []f.Memory{}, nil
}
func (r *stageStub) GetWorkflow(_ context.Context, owner, id string) (f.Workflow, error) {
	if owner != r.snapshot.Workflow.OwnerID || id != r.snapshot.Workflow.ID {
		return f.Workflow{}, f.Fail("RESOURCE_NOT_FOUND", "not found")
	}
	return r.snapshot.Workflow, nil
}
func (r *stageStub) LearnerSnapshot(_ context.Context, s f.Scope, id string) (stage.Snapshot, error) {
	if s.OwnerID != r.snapshot.Workflow.OwnerID || id != r.snapshot.Workflow.ID {
		return stage.Snapshot{}, f.Fail("RESOURCE_NOT_FOUND", "not found")
	}
	return stage.Clone(r.snapshot), nil
}
func (r *stageStub) CommitLearnerStage(_ context.Context, c stage.Commit) (stage.Receipt, error) {
	r.commits++
	r.snapshot = c.Desired
	r.receipt = stage.Receipt{SessionID: c.SessionID, Revision: c.Revision, Operation: c.Operation, Status: "COMMITTED", WorkflowID: c.Desired.Workflow.ID, WorkflowVersion: 1, Verified: true, MemoryIDs: []string{}, IDMap: map[string]string{}}
	return r.receipt, nil
}
func (r *stageStub) LearnerStageReceipt(context.Context, string, string) (stage.Receipt, error) {
	if r.receipt.SessionID == "" {
		return stage.Receipt{}, f.Fail("RESOURCE_NOT_FOUND", "none")
	}
	return r.receipt, nil
}
func stagingFixture() *stageStub {
	a := testAgent("learner", "owner")
	a.Type = "learner"
	a.EnabledTools = []string{"workflow_create", "workflow_get", "workflow_update", "memory_create", "memory_get", "memory_search", "memory_update"}
	a.Permissions = []string{"workflow.read", "workflow.write", "memory.read", "memory.write"}
	return &stageStub{repoStub: &repoStub{agents: map[string]f.AgentInstance{a.ID: a}, runs: map[string]f.RunRecord{}}, snapshot: stage.Snapshot{Memories: []f.Memory{}, Relations: []f.MemoryRelation{}}}
}
func stagedCalls(name, args string) f.ModelResponse {
	return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: name, Type: "function", Function: f.FunctionCall{Name: name, Arguments: args}}}}, FinishReason: "tool_calls"}
}
func stagingState(t *testing.T, r f.ModelRequest) map[string]any {
	t.Helper()
	last := r.Messages[len(r.Messages)-1].Content
	const marker = "data):\n"
	i := strings.Index(last, marker)
	if i < 0 {
		t.Fatal(last)
	}
	raw := strings.SplitN(last[i+len(marker):], "\n", 2)[0]
	var out map[string]any
	if json.Unmarshal([]byte(raw), &out) != nil {
		t.Fatal(raw)
	}
	return out
}
func readyStage(t *testing.T, r f.ModelRequest, entity json.RawMessage) f.ModelResponse {
	t.Helper()
	state := stagingState(t, r)
	return f.ModelResponse{Message: f.Message{Role: "assistant", Content: string(mustStageJSON(map[string]any{"protocol": stage.Protocol, "session_id": state["session_id"], "path": state["path"], "request_id": nil, "item_id": nil, "parent_item_id": nil, "workflow_id": state["workflow_id"], "base_version": state["base_version"], "entity": entity, "status": "READY", "operation": "CREATE", "completion": map[string]any{"marker": "READY_TO_COMMIT", "session_id": state["session_id"], "staging_revision": state["staging_revision"]}, "summary": "prepared", "reason": "evidence inspected", "evidence_refs": []any{}, "changes": []any{}, "questions": []any{}, "limitations": []any{}}))}, FinishReason: "stop"}
}
func previousToolError(mode string) string {
	switch mode {
	case "unknown tool then ready":
		return "TOOL_NOT_FOUND"
	case "denied read then ready":
		return "PERMISSION_DENIED"
	}
	return ""
}
func TestLearnerStagingRunCommitsOnlyCompletedTarget(t *testing.T) {
	for _, mode := range []string{"ready", "format", "missing marker", "unresolved schema", "recovered schema", "unknown tool then ready", "denied read then ready", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			repo := stagingFixture()
			round := 0
			entity := json.RawMessage(`{"kind":"course","id":"specific-course","name":"Calculus"}`)
			ctx, cancel := context.WithCancel(WithLearnerStage(context.Background(), "owner", "INITIALIZE", ""))
			defer cancel()
			service, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"learner": fnModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
				round++
				if repo.commits != 0 {
					t.Fatal("production changed before model final")
				}
				auditCode := previousToolError(mode)
				if round == 1 && auditCode != "" {
					if auditCode == "PERMISSION_DENIED" {
						return stagedCalls("data_query", `{"dataset":"platform_records","fields":["updated_at"]}`), nil
					}
					return stagedCalls("list_skills", `{}`), nil
				}
				if round == 1 || auditCode != "" && round == 2 {
					if mode == "unresolved schema" || mode == "recovered schema" {
						return stagedCalls("workflow_create", `{"name":"Calculus"}`), nil
					}
					return stagedCalls("workflow_create", `{"name":"Calculus","status":"active","definition":{"entity":{"kind":"course","id":"specific-course","name":"Calculus"}}}`), nil
				}
				if mode == "recovered schema" && round == 2 {
					return stagedCalls("workflow_create", `{"name":"Calculus","status":"active","definition":{"entity":{"kind":"course","id":"specific-course","name":"Calculus"}}}`), nil
				}
				if mode == "ready" && round == 2 || (mode == "recovered schema" || auditCode != "") && round == 3 {
					return stagedCalls("memory_create", `{"title":"course","content":"Evidence-backed course overview","source_refs":["record-1"]}`), nil
				}
				if mode == "cancel" {
					cancel()
					return readyStage(t, r, entity), nil
				}
				if mode == "format" {
					return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "# finished"}, FinishReason: "stop"}, nil
				}
				response := readyStage(t, r, entity)
				if mode == "missing marker" {
					var v map[string]any
					_ = json.Unmarshal([]byte(response.Message.Content), &v)
					v["completion"] = nil
					response.Message.Content = string(mustStageJSON(v))
				}
				return response, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			var terminal string
			service.Runtime.Observer = func(_ context.Context, _ f.RunRecord, state, out string) {
				if state == "succeeded" || state == "failed" {
					terminal = out
				}
			}
			result, err := service.Run(ctx, "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
			success := mode == "ready" || mode == "recovered schema" || previousToolError(mode) != ""
			if success {
				if err != nil || repo.commits != 1 || !strings.Contains(result.Output, `"status":"APPLIED"`) || terminal != result.Output {
					t.Fatal(result, err, repo.commits, terminal)
				}
				if code := previousToolError(mode); code != "" {
					run := repo.runs[result.RunID]
					if len(run.ToolExecutions) == 0 || run.ToolExecutions[0].Error == nil || run.ToolExecutions[0].Error.Code != code {
						t.Fatal("tool error missing from the audit")
					}
				}
				if mode == "ready" && (len(repo.snapshot.Memories) != 1 || repo.snapshot.Memories[0].WorkflowID != repo.snapshot.Workflow.ID) {
					t.Fatal("memory not bound to target")
				}
			} else if err == nil || repo.commits != 0 {
				t.Fatal("incomplete run committed", mode, result, err, repo.commits)
			}
		})
	}
}
func TestLearnerStagingTimeoutKeepsOriginalFailureAndFinalizesOnce(t *testing.T) {
	repo := stagingFixture()
	s, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"learner": fnModel(func(context.Context, f.ModelRequest) (f.ModelResponse, error) {
		return f.ModelResponse{}, context.DeadlineExceeded
	})}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithLearnerStage(context.Background(), "owner", "INITIALIZE", "")
	result, err := s.Run(ctx, "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	errCode(t, err, "TIMEOUT")
	record := repo.runs[result.RunID]
	if record.Status != "failed" || record.Error == nil || record.Error.Code != "TIMEOUT" || result.Error == nil || result.Error.Code != "TIMEOUT" {
		t.Fatal(result, err, record)
	}
	if repo.commits != 0 {
		t.Fatal("timeout committed")
	}
}

func TestLearnerStagingStateExposesDraftCompletionBlocker(t *testing.T) {
	repo := stagingFixture()
	session := stage.New(repo, repo, "owner", "learner", "INITIALIZE", "")
	_, err := session.SaveWorkflow(context.Background(), f.Scope{OwnerID: "owner", AgentID: "learner"}, f.Workflow{Name: "Calculus", Status: "draft", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"specific-course","name":"Calculus"}}`)}, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(session.State())
	var state struct {
		Status   string   `json:"workflow_status"`
		Blockers []string `json:"completion_blockers"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Status != "draft" || len(state.Blockers) != 2 {
		t.Fatal("draft and missing knowledge were hidden", string(raw))
	}
	w := session.View.Workflow
	w.Status = "active"
	if _, err = session.SaveWorkflow(context.Background(), f.Scope{OwnerID: "owner", AgentID: "learner"}, w, false); err != nil {
		t.Fatal(err)
	}
	if _, err = session.SaveMemory(context.Background(), f.Scope{OwnerID: "owner", AgentID: "learner"}, f.Memory{Title: "course", Content: "sourced course context", SourceRefs: json.RawMessage(`["record-1"]`)}, true); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(session.State())
	if json.Unmarshal(raw, &state) != nil || state.Status != "active" || len(state.Blockers) != 0 {
		t.Fatal("ready structure remained blocked", string(raw))
	}
}

func TestLearnerStagingSharedConcurrencyGate(t *testing.T) {
	repo := stagingFixture()
	a := repo.agents["learner"]
	a.Limits.MaxConcurrent = 1
	repo.agents[a.ID] = a
	entered, release := make(chan struct{}), make(chan struct{})
	s, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"learner": fnModel(func(context.Context, f.ModelRequest) (f.ModelResponse, error) {
		close(entered)
		<-release
		return done(), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithLearnerStage(context.Background(), "owner", "INITIALIZE", "")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = s.Run(ctx, "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	}()
	<-entered
	_, err = s.Run(ctx, "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	errCode(t, err, "RATE_LIMITED")
	close(release)
	<-finished
}

func TestLearnerStagingRejectsUntrustedJSONRole(t *testing.T) {
	repo := stagingFixture()
	s, err := New(Dependencies{Repository: repo})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Run(context.Background(), "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{"role":"filter","path":"INITIALIZE"}`)})
	errCode(t, err, "PERMISSION_DENIED")
}
func TestLearnerStageSelectedTargetIncludesAllPrivateStates(t *testing.T) {
	repo := stagingFixture()
	repo.snapshot = stage.Snapshot{Workflow: f.Workflow{ID: "wf", OwnerID: "owner", Version: 2, Status: "active", Definition: json.RawMessage(`{}`)}, Memories: []f.Memory{{ID: "archived", OwnerID: "owner", WorkflowID: "wf", Visibility: "PRI", Title: "old", Content: "old state", Status: "archived"}}, Relations: []f.MemoryRelation{}}
	s := stage.New(repo, repo, "owner", "learner", "REVISE", "wf")
	if err := s.Select(context.Background(), "wf"); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetMemory(context.Background(), f.Scope{OwnerID: "owner", AgentID: "learner"}, "archived")
	if err != nil || m.Status != "archived" {
		t.Fatal(m, err)
	}
	if err := s.Select(context.Background(), "foreign"); err == nil {
		t.Fatal("scope switched")
	}
	m.WorkflowID = "foreign"
	if _, err := s.SaveMemory(context.Background(), f.Scope{OwnerID: "owner", AgentID: "learner"}, m, false); err == nil {
		t.Fatal("foreign write accepted")
	}
}
