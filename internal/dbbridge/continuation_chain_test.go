package dbbridge

import (
	f "academic/agent/framework"
	svc "academic/backend/internal/agent"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A new Service instance answers the question and runs the scheduled Worker;
// neither operation has an in-process pending-task map to fall back on.
func TestContinuationHandledChainAndScheduledReuse(t *testing.T) {
	for _, role := range []string{"chat", "filter"} {
		t.Run(role, func(t *testing.T) {
			db := testDB(t)
			ctx := context.Background()
			u := fixtureUser(t, db, "owner", model.RoleUser)
			reg := f.Registry{Repository: db.Agents()}
			d := definition(role, "parent", []string{"agent_dispatch"}, []string{"agent.dispatch", "workflow.read"})
			d.Type = role
			parent, e := reg.Create(ctx, u.ID, d)
			must(t, e)
			d = definition("worker", "worker", []string{"agent_dispatch"}, []string{"agent.dispatch", "workflow.read"})
			d.Type = "worker"
			worker, e := reg.Create(ctx, u.ID, d)
			must(t, e)
			w, e := db.Agents().SaveWorkflow(ctx, f.Scope{OwnerID: u.ID, AgentID: parent.ID}, f.Workflow{Name: "course", Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"x","name":"Math"}}`)}, true)
			must(t, e)
			d = definition("learner", "learner", nil, nil)
			d.Type = "learner"
			learner, e := reg.Create(ctx, u.ID, d)
			must(t, e)
			pCalls, wCalls, lCalls := 0, 0, 0
			final := func(raw string) f.ModelResponse {
				return f.ModelResponse{Message: f.Message{Role: "assistant", Content: raw}, FinishReason: "stop"}
			}
			models := map[string]f.ModelProvider{"parent": &memoryModel{next: func(req f.ModelRequest) f.ModelResponse {
				pCalls++
				if pCalls == 1 {
					raw, _ := json.Marshal(f.AgentDispatchInput{Target: worker.ID, Task: "track math", Workflow: &f.WorkflowSelection{ID: w.ID, Version: w.Version}, Context: json.RawMessage(`{"event":{"event_id":"e","source":{"platform":"manual"},"content":"track math","evidence_refs":[]}}`)})
					return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "d", Type: "function", Function: f.FunctionCall{Name: "agent_dispatch", Arguments: string(raw)}}}}, FinishReason: "tool_calls"}
				}
				if pCalls == 3 {
					last := req.Messages[len(req.Messages)-1]
					if !strings.Contains(last.Content, "math confirmed") {
						t.Fatal("missing completed child handoff", last)
					}
				}
				if role == "chat" {
					return final(`{"text":"summary","cards":[],"interaction":null}`)
				}
				return final(`{"status":"handled"}`)
			}}, "worker": &memoryModel{next: func(req f.ModelRequest) f.ModelResponse {
				wCalls++
				switch wCalls {
				case 1:
					if role == "chat" {
						raw, _ := json.Marshal(f.AgentDispatchInput{Target: learner.ID, Task: "clarify priority"})
						return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "learn", Type: "function", Function: f.FunctionCall{Name: "agent_dispatch", Arguments: string(raw)}}}}, FinishReason: "tool_calls"}
					}
					return final(`{"text":"select date","interaction":{"type":"CHOICE","question":"Which date?","options":[{"id":"m","label":"Monday"}]}}`)
				default:
					if role == "chat" && wCalls == 2 {
						return final(`{"text":"awaiting learner answer","interaction":null}`)
					}
					raw, _ := json.Marshal(req.Messages)
					if !strings.Contains(string(raw), "Which date?") || !strings.Contains(string(raw), "track math") {
						t.Fatal("missing original Worker context")
					}
					return final(`{"text":"math confirmed","summary":"math confirmed","interaction":null}`)
				}
			}}}
			models["learner"] = &memoryModel{next: func(req f.ModelRequest) f.ModelResponse {
				lCalls++
				s := strings.SplitN(req.Messages[len(req.Messages)-1].Content, "data):\n", 2)
				if len(s) != 2 {
					t.Fatal("missing staging state")
				}
				var st map[string]any
				must(t, json.Unmarshal([]byte(strings.SplitN(s[1], "\n", 2)[0]), &st))
				status := "NEEDS_INFO"
				var interaction any = map[string]any{"type": "CHOICE", "question": "Which date?", "options": []map[string]string{{"id": "m", "label": "Monday"}}}
				if lCalls > 1 {
					status = "REJECTED"
					interaction = nil
				}
				raw, _ := json.Marshal(map[string]any{"protocol": "learner_staging_v2", "session_id": st["session_id"], "path": st["path"], "request_id": nil, "item_id": nil, "parent_item_id": nil, "workflow_id": st["workflow_id"], "base_version": st["base_version"], "entity": nil, "status": status, "operation": "NONE", "completion": nil, "summary": "no new facts", "reason": "fixture", "evidence_refs": []any{}, "changes": []any{}, "questions": []any{}, "interaction": interaction, "limitations": []any{}})
				return final(string(raw))
			}}
			s, e := svc.New(svc.Dependencies{Repository: db.Agents(), Providers: models})
			must(t, e)
			channel := c.ChA
			if role == "chat" {
				channel = c.ChB
			}
			ctx = c.WithSource(ctx, u.ID, channel, "thread")
			_, e = s.Run(ctx, u.ID, f.RunRequest{AgentID: parent.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
			must(t, e)
			pending, e := s.PendingQuestions(ctx, u.ID, channel, "thread")
			must(t, e)
			if len(pending) != 1 || pending[0].Role != map[string]string{"chat": "learner", "filter": "worker"}[role] || len(pending[0].Options) != 1 {
				t.Fatalf("wrong pending %#v", pending)
			}
			q := pending[0]
			fresh, e := svc.New(svc.Dependencies{Repository: db.Agents(), Providers: models})
			must(t, e)
			if _, e = fresh.Answer(ctx, u.ID, q.ID, "unoffered choice"); e == nil {
				t.Fatal("invalid option consumed the pending question")
			}
			res, e := fresh.Answer(ctx, u.ID, q.ID, "Monday")
			must(t, e)
			again, e := fresh.Answer(ctx, u.ID, q.ID, "Monday")
			must(t, e)
			if pCalls != 3 || wCalls != map[string]int{"chat": 3, "filter": 2}[role] || res.RunID != again.RunID {
				t.Fatalf("incorrect restore/replay: parent=%d worker=%d", pCalls, wCalls)
			}
			origin := q.RunID
			if role == "chat" {
				ar, err := db.Agents().(c.Store).LoadContinuation(ctx, u.ID, origin)
				must(t, err)
				origin = ar.Transcript.Parent
			}
			_, e = fresh.RunScheduledWorker(context.Background(), u.ID, origin, json.RawMessage(`{"task":"weekly check"}`))
			must(t, e)
			if pCalls != 3 || wCalls != map[string]int{"chat": 4, "filter": 3}[role] {
				t.Fatal("scheduled Worker woke its original parent")
			}
		})
	}
}
