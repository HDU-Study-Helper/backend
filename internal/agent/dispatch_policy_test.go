package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
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

func TestFilterLearnerWorkflowPolicyThroughRealBridge(t *testing.T) {
	for _, tc := range []struct {
		name, callerType, task, context string
		bound, reject                   bool
	}{
		{"course execution", "filter", "execute course event", `{}`, true, true},
		{"management with workflow", "filter", "update workflow", `{"request_type":"UPDATE","target_workflow_id":"wf"}`, true, true},
		{"management without workflow", "filter", "manage workflow", `{"target_workflow_id":"wf"}`, false, false},
		{"worker feedback", "worker", "feedback", `{}`, true, false},
		{"legacy other caller", "assistant", "legacy task", `{}`, true, false},
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
							if result.OK || result.Error == nil || result.Error.Code != "INVALID_ARGUMENT" {
								t.Fatalf("want actionable policy rejection, got %+v", result)
							}
							for _, guidance := range []string{"Worker", "target_agent_id", "context.target_workflow_id"} {
								if !strings.Contains(result.Error.Message, guidance) {
									t.Fatalf("missing %q in %q", guidance, result.Error.Message)
								}
							}
						} else if !result.OK {
							t.Fatalf("compatible dispatch rejected: %+v", result)
						}
						return done(), nil
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
					if !tc.bound && !strings.Contains(request.Messages[len(request.Messages)-1].Content, "target_workflow_id") {
						t.Fatal("management target context was lost")
					}
					return done(), nil
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
