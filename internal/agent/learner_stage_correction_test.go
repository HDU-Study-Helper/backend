package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
)

func TestLearnerStagingCorrectsFinalWithinSameSession(t *testing.T) {
	for _, mode := range []string{"format", "active", "entity", "base version", "bounded", "format then semantic"} {
		t.Run(mode, func(t *testing.T) {
			repo := stagingFixture()
			round := 0
			finals := 0
			initialSession := ""
			workflowID := ""
			entity := json.RawMessage(`{"kind":"course","id":"specific-course","name":"Calculus"}`)
			service, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"learner": fnModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
				round++
				state := stagingState(t, r)
				session, _ := state["session_id"].(string)
				if initialSession == "" {
					initialSession = session
				} else if initialSession != session {
					t.Fatal("correction restarted staging session")
				}
				if repo.commits != 0 {
					t.Fatal("committed before final validation")
				}
				if round == 1 {
					status := "active"
					if mode == "active" {
						status = "draft"
					}
					return stagedCalls("workflow_create", `{"name":"Calculus","status":"`+status+`","definition":{"entity":{"kind":"course","id":"specific-course","name":"Calculus"}}}`), nil
				}
				if round == 2 {
					workflowID, _ = state["workflow_id"].(string)
					return stagedCalls("memory_create", `{"title":"course","content":"Evidence-backed course overview","source_refs":["record-1"]}`), nil
				}
				if mode == "active" && round == 4 {
					if len(r.Tools) == 0 {
						t.Fatal("semantic state repair has no tools")
					}
					return stagedCalls("workflow_update", string(mustStageJSON(map[string]any{"workflow_id": workflowID, "version": 1, "status": "active", "name": "Calculus", "definition": map[string]any{"entity": json.RawMessage(entity)}}))), nil
				}
				finals++
				response := readyStage(t, r, entity)
				switch mode {
				case "format":
					if finals == 1 {
						response.Message.Content = "{broken"
					} else if len(r.Tools) != 0 {
						t.Fatal("format repair exposed tools")
					}
				case "entity":
					if finals == 1 {
						response = readyStage(t, r, json.RawMessage(`{"kind":"course","id":"wrong","name":"Wrong"}`))
					} else if len(r.Tools) == 0 {
						t.Fatal("semantic tools unavailable")
					}
				case "base version":
					if finals == 1 {
						var out map[string]any
						_ = json.Unmarshal([]byte(response.Message.Content), &out)
						out["base_version"] = 99
						response.Message.Content = string(mustStageJSON(out))
					}
				case "bounded":
					response.Message.Content = "{broken"
				case "format then semantic":
					if finals == 1 {
						response.Message.Content = "{broken"
					} else if finals == 2 {
						if len(r.Tools) != 0 {
							t.Fatal("first correction must be format-only")
						}
						response = readyStage(t, r, json.RawMessage(`{"kind":"course","id":"wrong","name":"Wrong"}`))
					} else if len(r.Tools) == 0 {
						t.Fatal("semantic tools not restored after syntax fixed")
					}
				}
				return response, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Run(WithLearnerStage(context.Background(), "owner", "INITIALIZE", ""), "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
			record := repo.runs[result.RunID]
			if mode == "bounded" {
				errCode(t, err, "INVALID_LEARNER_RESULT")
				if repo.commits != 0 || finals != 3 || record.ModelCalls != 5 {
					t.Fatal(repo.commits, finals, record)
				}
				return
			}
			if err != nil || repo.commits != 1 || len(repo.snapshot.Memories) != 1 || repo.snapshot.Workflow.ID != workflowID || !strings.Contains(result.Output, `"status":"APPLIED"`) {
				t.Fatal(result, err, repo.commits, repo.snapshot)
			}
			expected := 1
			if mode == "format then semantic" {
				expected = 2
			}
			if len(record.OutputCorrections) != expected || record.Status != "succeeded" {
				t.Fatal(record)
			}
			creates := 0
			for _, tool := range record.ToolExecutions {
				if tool.Name == "workflow_create" {
					creates++
				}
			}
			if creates != 1 {
				t.Fatal("correction repeated initialization", record.ToolExecutions)
			}
		})
	}
}

func TestLearnerFinalFatalFailuresDoNotRequestCorrection(t *testing.T) {
	for _, code := range []string{"CONFLICT", "TIMEOUT", "CANCELLED", "EXECUTION_LIMIT_EXCEEDED"} {
		repo := stagingFixture()
		session := stage.New(repo, repo, "owner", "learner", "INITIALIZE", "")
		session.View.Workflow = f.Workflow{ID: "wf", Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"course","name":"Course"}}`)}
		session.View.Memories = []f.Memory{{ID: "memory", SourceRefs: json.RawMessage(`["record"]`)}}
		session.ToolFailures = map[string]*f.Error{"data_query": f.Fail(code, "fatal operation")}
		raw := mustStageJSON(map[string]any{"protocol": stage.Protocol, "session_id": session.ID, "path": "INITIALIZE", "request_id": nil, "item_id": nil, "parent_item_id": nil, "workflow_id": "wf", "base_version": nil, "entity": map[string]string{"kind": "course", "id": "course", "name": "Course"}, "status": "READY", "operation": "CREATE", "completion": map[string]any{"marker": "READY_TO_COMMIT", "session_id": session.ID, "staging_revision": session.Revision}, "summary": "ready", "reason": "ready", "evidence_refs": []any{}, "changes": []any{}, "questions": []any{}, "limitations": []any{}})
		_, err := validateLearnerFinal(context.Background(), session, string(raw))
		if f.SafeError(err).Code != code {
			t.Fatal(code, err)
		}
	}
}

func TestKnownRoleJSONObjectCorrectionIsFormatOnly(t *testing.T) {
	repo := stagingFixture()
	a := repo.agents["learner"]
	a.Type = "worker"
	repo.agents[a.ID] = a
	round := 0
	s, err := New(Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"learner": fnModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		round++
		if round == 1 {
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "not json"}, FinishReason: "stop"}, nil
		}
		if len(r.Tools) != 0 {
			t.Fatal("known-role syntax correction exposed tools")
		}
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: `{"status":"COMPLETED"}`}, FinishReason: "stop"}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Run(context.Background(), "owner", f.RunRequest{AgentID: a.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	if err != nil || round != 2 || len(repo.runs[result.RunID].OutputCorrections) != 1 {
		t.Fatal(result, err, round)
	}
}
