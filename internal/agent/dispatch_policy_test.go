package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
)

type dispatchPolicyRepo struct {
	*repoStub
	workflow f.Workflow
}

func (r *dispatchPolicyRepo) ListAgents(_ context.Context, owner string, limit int, after string) ([]f.AgentInstance, error) {
	out := []f.AgentInstance{}
	for _, a := range r.agents {
		if a.OwnerID == owner && a.ID > after {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *dispatchPolicyRepo) GetWorkflow(_ context.Context, owner, id string) (f.Workflow, error) {
	if r.workflow.OwnerID != owner || r.workflow.ID != id {
		return f.Workflow{}, f.Fail("RESOURCE_NOT_FOUND", "not owned")
	}
	return r.workflow, nil
}

func (r *dispatchPolicyRepo) SearchMemory(context.Context, f.Scope, f.MemorySearch) ([]f.Memory, error) {
	return []f.Memory{}, nil
}
func (r *dispatchPolicyRepo) LearnerSnapshot(_ context.Context, s f.Scope, id string) (stage.Snapshot, error) {
	if s.OwnerID != r.workflow.OwnerID || id != r.workflow.ID {
		return stage.Snapshot{}, f.Fail("RESOURCE_NOT_FOUND", "not owned")
	}
	return stage.Snapshot{Workflow: r.workflow, Memories: []f.Memory{}, Relations: []f.MemoryRelation{}}, nil
}
func (*dispatchPolicyRepo) CommitLearnerStage(context.Context, stage.Commit) (stage.Receipt, error) {
	panic("blocked mock must not commit")
}
func (*dispatchPolicyRepo) LearnerStageReceipt(context.Context, string, string) (stage.Receipt, error) {
	return stage.Receipt{}, f.Fail("RESOURCE_NOT_FOUND", "none")
}

func TestFilterLearnerWorkflowPolicyThroughRealBridge(t *testing.T) {
	for _, tc := range []struct {
		name, callerType, task, context string
		bound, reject                   bool
	}{
		{"course execution", "filter", "execute course event", `{}`, true, true},
		{"management with workflow", "filter", "update workflow", `{"request_type":"UPDATE","target_workflow_id":"wf"}`, true, true},
		{"management without workflow", "filter", "manage workflow", `{"target_workflow_id":"wf"}`, false, false},
		{"worker feedback", "worker", "feedback", `{}`, true, false},
		{"untrusted other caller", "assistant", "legacy task", `{}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller, learner := testAgent("caller", "owner"), testAgent("learner", "owner")
			caller.Type, learner.Type = tc.callerType, "learner"
			caller.Permissions = append(caller.Permissions, "workflow.read")
			repo := &dispatchPolicyRepo{
				repoStub: &repoStub{agents: map[string]f.AgentInstance{caller.ID: caller, learner.ID: learner}, runs: map[string]f.RunRecord{}},
				workflow: f.Workflow{ID: "wf", OwnerID: "owner", Version: 1, Status: "active", Definition: json.RawMessage(`{}`)},
			}
			childCalled, feedbackSeen := false, false
			service, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{
				"caller": fnModel(func(_ context.Context, request f.ModelRequest) (f.ModelResponse, error) {
					last := request.Messages[len(request.Messages)-1]
					if last.Role == "tool" {
						feedbackSeen = true
						var result f.ToolResult
						if err := json.Unmarshal([]byte(last.Content), &result); err != nil {
							t.Fatal(err)
						}
						if tc.reject {
							expected := "INVALID_ARGUMENT"
							if tc.callerType == "assistant" {
								expected = "PERMISSION_DENIED"
							}
							if result.OK || result.Error == nil || result.Error.Code != expected {
								t.Fatalf("want actionable policy rejection, got %+v", result)
							}
							guidances := []string{"Worker", "target_agent_id", "context.target_workflow_id"}
							if tc.callerType == "assistant" {
								guidances = []string{"Filter", "Worker"}
							}
							for _, guidance := range guidances {
								if !strings.Contains(result.Error.Message, guidance) {
									t.Fatalf("missing %q in %q", guidance, result.Error.Message)
								}
							}
						} else if !result.OK {
							t.Fatalf("compatible dispatch rejected: %+v", result)
						}
						return f.ModelResponse{Message: f.Message{Role: "assistant", Content: `{"status":"COMPLETED"}`}, FinishReason: "stop"}, nil
					}
					var childContext map[string]any
					if err := json.Unmarshal([]byte(tc.context), &childContext); err != nil {
						t.Fatal(err)
					}
					childContext["event"] = map[string]any{"event_id": "evt", "content": "notice", "source": map[string]any{"platform": "manual"}, "evidence_refs": []any{}}
					args := map[string]any{"target_agent_id": "learner", "task": tc.task, "context": childContext}
					if tc.bound {
						args["workflow"] = map[string]any{"workflow_id": "wf", "version": 1}
					}
					raw, _ := json.Marshal(args)
					return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "dispatch", Type: "function", Function: f.FunctionCall{Name: "agent_dispatch", Arguments: string(raw)}}}}, FinishReason: "tool_calls"}, nil
				}),
				"learner": fnModel(func(_ context.Context, request f.ModelRequest) (f.ModelResponse, error) {
					childCalled = true
					if tc.reject {
						t.Fatal("rejected dispatch reached Learner model")
					}
					if !tc.bound {
						found := false
						for _, m := range request.Messages {
							found = found || strings.Contains(m.Content, "target_workflow_id")
						}
						if !found {
							t.Fatal("management target context was lost")
						}
					}
					state := stagingState(t, request)
					return f.ModelResponse{Message: f.Message{Role: "assistant", Content: string(mustStageJSON(map[string]any{"protocol": stage.Protocol, "session_id": state["session_id"], "path": state["path"], "request_id": nil, "item_id": nil, "parent_item_id": nil, "workflow_id": state["workflow_id"], "base_version": state["base_version"], "entity": nil, "status": "NEEDS_INFO", "operation": "NONE", "completion": nil, "summary": "blocked without writes", "reason": "awaiting evidence", "evidence_refs": []any{}, "changes": []any{}, "questions": []any{}, "limitations": []any{}}))}, FinishReason: "stop"}, nil
				}),
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Run(context.Background(), "owner", f.RunRequest{AgentID: caller.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			if !feedbackSeen || childCalled == tc.reject {
				t.Fatalf("unexpected dispatch outcome: feedback=%v child=%v", feedbackSeen, childCalled)
			}
			for _, run := range repo.runs {
				if run.AgentID == learner.ID && run.WorkflowID != map[bool]string{true: "wf", false: ""}[tc.bound] {
					t.Fatalf("incorrect child workflow scope: %+v", run)
				}
				if tc.reject && run.AgentID == learner.ID {
					t.Fatal("rejected dispatch created Learner run")
				}
			}
		})
	}
}

func TestDispatchPolicyPreservesOwnerLookupAndWorkerRequirement(t *testing.T) {
	for _, tc := range []struct {
		name, callerOwner, targetOwner, targetType, workflow, code string
	}{
		{"foreign target", "owner", "other", "learner", "wf", "AGENT_NOT_FOUND"},
		{"foreign caller", "other", "owner", "learner", "wf", "AGENT_NOT_FOUND"},
		{"filter worker requires workflow", "owner", "owner", "worker", "", "INVALID_ARGUMENT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller, target := testAgent("caller", tc.callerOwner), testAgent("target", tc.targetOwner)
			caller.Type, target.Type = "filter", tc.targetType
			repo := &repoStub{agents: map[string]f.AgentInstance{caller.ID: caller, target.ID: target}, runs: map[string]f.RunRecord{}}
			service, err := New(Dependencies{Repository: repo})
			if err != nil {
				t.Fatal(err)
			}
			_, err = (DispatchBridge{Port: service}).Dispatch(context.Background(), f.ExecutionContext{
				Scope: f.Scope{OwnerID: "owner", AgentID: caller.ID, WorkflowID: tc.workflow}, Permissions: []string{"agent.dispatch"},
			}, f.RunRequest{AgentID: target.ID, Input: json.RawMessage(`{}`)})
			errCode(t, err, tc.code)
			if len(repo.runs) != 0 {
				t.Fatal("rejected dispatch created a run")
			}
		})
	}
}
