package dbbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
	agentservice "academic/backend/internal/agent"
)

type learnerAuditModel func(context.Context, f.ModelRequest) (f.ModelResponse, error)

func (fn learnerAuditModel) Complete(ctx context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	return fn(ctx, r)
}

func TestLearnerStagePostgresHostFinalizesRunOnce(t *testing.T) {
	for _, mode := range []string{"timeout", "format", "blocked", "commit", "corrected format", "corrected active"} {
		t.Run(mode, func(t *testing.T) {
			db, scope, _ := learnerStageFixture(t)
			round := 0
			commitMode := mode == "commit" || mode == "corrected format" || mode == "corrected active"
			s, err := agentservice.New(agentservice.Dependencies{Repository: db.Agents(), Providers: map[string]f.ModelProvider{"mock": learnerAuditModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
				if mode == "timeout" {
					return f.ModelResponse{}, context.DeadlineExceeded
				}
				round++
				if commitMode && round <= 2 {
					name, args := "workflow_create", `{"name":"Calculus audit","status":"active","definition":{"entity":{"kind":"course","id":"audit-calculus","name":"Calculus"}}}`
					if mode == "corrected active" {
						args = strings.Replace(args, `"status":"active"`, `"status":"draft"`, 1)
					}
					if round == 2 {
						name, args = "memory_create", `{"title":"course fact","content":"verified synthetic course","source_refs":["fixture-source"]}`
					}
					return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: name, Type: "function", Function: f.FunctionCall{Name: name, Arguments: args}}}}, FinishReason: "tool_calls"}, nil
				}
				output := "# incomplete"
				if mode == "corrected format" && round == 3 {
					return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "{broken"}, FinishReason: "stop"}, nil
				}
				if mode == "corrected format" && round == 4 && len(r.Tools) != 0 {
					t.Fatal("format correction exposed mutation tools")
				}
				if mode == "blocked" || commitMode {
					last := r.Messages[len(r.Messages)-1].Content
					const marker = "data):\n"
					pos := strings.Index(last, marker)
					if pos < 0 {
						t.Fatal(last)
					}
					var state map[string]any
					if err := json.Unmarshal([]byte(strings.SplitN(last[pos+len(marker):], "\n", 2)[0]), &state); err != nil {
						t.Fatal(err)
					}
					raw, _ := json.Marshal(map[string]any{"protocol": "learner_staging_v2", "session_id": state["session_id"], "path": state["path"], "request_id": nil, "item_id": nil, "parent_item_id": nil, "workflow_id": "", "base_version": nil, "entity": nil, "status": "NEEDS_INFO", "operation": "NONE", "completion": nil, "summary": "missing evidence", "reason": "course cannot be identified", "evidence_refs": []any{}, "changes": []any{}, "questions": []any{"which course?"}, "limitations": []any{}})
					if mode == "corrected active" && round == 4 {
						if len(r.Tools) == 0 {
							t.Fatal("semantic repair tools unavailable")
						}
						args, _ := json.Marshal(map[string]any{"workflow_id": state["workflow_id"], "version": 1, "name": "Calculus audit", "status": "active", "definition": map[string]any{"entity": map[string]string{"kind": "course", "id": "audit-calculus", "name": "Calculus"}}})
						return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "update", Type: "function", Function: f.FunctionCall{Name: "workflow_update", Arguments: string(args)}}}}, FinishReason: "tool_calls"}, nil
					}
					if commitMode {
						var final map[string]any
						_ = json.Unmarshal(raw, &final)
						final["status"] = "READY"
						final["operation"] = "CREATE"
						final["workflow_id"] = state["workflow_id"]
						final["entity"] = map[string]any{"kind": "course", "id": "audit-calculus", "name": "Calculus"}
						final["completion"] = map[string]any{"marker": "READY_TO_COMMIT", "session_id": state["session_id"], "staging_revision": state["staging_revision"]}
						final["questions"] = []any{}
						raw, _ = json.Marshal(final)
					}
					output = string(raw)
				}
				return f.ModelResponse{Message: f.Message{Role: "assistant", Content: output}, FinishReason: "stop"}, nil
			})}})
			must(t, err)
			result, err := s.Run(agentservice.WithLearnerStage(context.Background(), scope.OwnerID, "INITIALIZE", ""), scope.OwnerID, f.RunRequest{AgentID: scope.AgentID, Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
			expected := ""
			if mode == "timeout" {
				expected = "TIMEOUT"
			} else if mode == "format" {
				expected = "INVALID_LEARNER_RESULT"
			}
			if expected != "" {
				code(t, err, expected)
			} else {
				must(t, err)
			}
			record, e := db.Agents().GetRun(context.Background(), scope.OwnerID, result.RunID)
			must(t, e)
			if expected != "" && (record.Error == nil || record.Error.Code != expected || record.Status != "failed") {
				t.Fatal(result, err, record)
			}
			if mode == "blocked" && record.Status != "succeeded" {
				t.Fatal(record)
			}
			var n int
			must(t, db.pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_workflows WHERE owner_user_id=$1`, scope.OwnerID).Scan(&n))
			if commitMode {
				if strings.HasPrefix(mode, "corrected") && len(record.OutputCorrections) != 1 {
					t.Fatal("missing final correction audit", record)
				}
				var receipts, memories int
				must(t, db.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM learner_stage_commits WHERE owner_user_id=$1),(SELECT count(*) FROM memories WHERE owner_user_id=$1)`, scope.OwnerID).Scan(&receipts, &memories))
				if receipts != 1 || memories != 1 {
					t.Fatal("correction duplicated formal work", receipts, memories)
				}
				if n != 1 || record.Status != "succeeded" || !strings.Contains(result.Output, `"status":"APPLIED"`) {
					t.Fatal("committed run audit failed", n, record, result, err)
				}
			} else if n != 0 {
				t.Fatal("incomplete run committed")
			}
		})
	}
}
