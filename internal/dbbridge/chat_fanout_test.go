package dbbridge

import (
	f "academic/agent/framework"
	"academic/backend/internal/actions"
	"academic/backend/internal/agent"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise a single public preference, immediate Chat fan-out, and the two independent
// reminder receipts through real tools/PG. No production data or external delivery.
func TestChatReminderFanoutCreatesDueNoticesWithoutAnotherModelCall(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "fanout-chat", model.RoleUser)
	must(t, db.EnableDefaultRoles(ctx))
	repo := db.Agents()
	ags, e := repo.ListAgents(ctx, u.ID, 10, "")
	must(t, e)
	roles := map[string]f.AgentInstance{}
	for _, a := range ags {
		a.Model = f.ModelConfig{Provider: a.Type, Model: "offline"}
		a.Limits.ContextBytes = 1 << 20
		a, e = repo.PutAgent(ctx, a)
		must(t, e)
		roles[a.Type] = a
	}
	var wfs []f.Workflow
	for _, name := range []string{"高数A", "线代B"} {
		def, _ := json.Marshal(map[string]any{"entity": map[string]string{"kind": "course", "name": name, "id": name}, "goal": "核查作业和提醒"})
		w, e := repo.SaveWorkflow(ctx, f.Scope{OwnerID: u.ID, AgentID: roles["learner"].ID}, f.Workflow{Name: name, Status: "active", Definition: def}, true)
		must(t, e)
		wfs = append(wfs, w)
	}
	thread, e := db.Chat().NewChatThread(ctx, u.ID)
	must(t, e)
	msg := "以后所有作业提前一天提醒我"
	turn, _, e := db.Chat().BeginChatTurn(ctx, u.ID, thread.ID, "req-1", "message", msg, "")
	must(t, e)
	ref := "chat_turn:" + turn.ID
	var mem string
	var chatRounds int
	var workers atomic.Int32
	call := func(name string, args any) f.ToolCall {
		b, _ := json.Marshal(args)
		return f.ToolCall{ID: f.NewID(), Type: "function", Function: f.FunctionCall{Name: name, Arguments: string(b)}}
	}
	calls := func(ts ...f.ToolCall) f.ModelResponse {
		return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: ts}, FinishReason: "tool_calls"}
	}
	stop := func(s string) f.ModelResponse {
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: s}, FinishReason: "stop"}
	}
	data := func(m f.Message) map[string]any {
		var r struct {
			OK   bool           `json:"ok"`
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(m.Content), &r) != nil || !r.OK {
			t.Fatalf("expected successful real tool response: %s", m.Content)
		}
		return r.Data
	}
	chat := chatTestModel(func(_ context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		chatRounds++
		switch chatRounds {
		case 1:
			return calls(call("memory_create", map[string]any{"visibility": "PUB", "title": "作业提醒偏好", "is_personal": true, "content": msg, "source_refs": []string{ref}})), nil
		case 2:
			mem = data(q.Messages[len(q.Messages)-1])["memory_id"].(string)
			var ts []f.ToolCall
			for _, w := range wfs {
				ts = append(ts, call("agent_dispatch", f.AgentDispatchInput{Target: roles["worker"].ID, Task: "核查该课并安排提醒", Workflow: &f.WorkflowSelection{ID: w.ID, Version: w.Version}, Context: json.RawMessage(`{"preference_memory_id":"` + mem + `","user_message_ref":"` + ref + `"}`)}))
			}
			return calls(ts...), nil
		default:
			for _, m := range q.Messages {
				if m.Role == "tool" && strings.Contains(m.Content, `"ok":false`) {
					t.Fatal(m.Content)
				}
			}
			return stop(`{"text":"两门课程的提醒已排期。","cards":[],"interaction":null}`), nil
		}
	})
	worker := chatTestModel(func(_ context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		workers.Add(1)
		last := q.Messages[len(q.Messages)-1]
		if last.Role == "tool" {
			_ = data(last)
			return stop(`{"text":"已排期","status":"COMPLETED","interaction":null,"artifacts":[]}`), nil
		}
		var wf string
		for _, w := range wfs {
			if strings.Contains(q.Messages[0].Content, w.ID) {
				wf = w.ID
				break
			}
		}
		if wf == "" || !strings.Contains(last.Content, mem) {
			t.Fatal("missing binding/preference handoff")
		}
		due := time.Now().UTC().Add(-time.Second)
		return calls(call("notice_create", map[string]any{"idempotency_key": "init-" + wf, "reminder_key": "assignment-" + wf, "title": "作业明天截止", "body": "检查作业", "due_at": due, "mode": "active", "reason": "用户明确要求所有作业提前一天提醒", "intent_turn_id": turn.ID, "intent_quote": msg, "preference_memory_id": mem, "evidence": []string{ref, mem}})), nil
	})
	svc, e := agent.New(agent.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"chat": chat, "worker": worker}})
	must(t, e)
	must(t, actions.RegisterTools(svc.Tools, db.Actions()))
	result, e := svc.Run(c.WithSource(ctx, u.ID, c.ChB, thread.ID), u.ID, f.RunRequest{AgentID: roles["chat"].ID, Trigger: f.UserMessage, Input: json.RawMessage(`{"message":"` + msg + `","user_message_ref":"` + ref + `"}`)})
	must(t, e)
	must(t, db.Chat().FinishChatTurn(ctx, u.ID, thread.ID, turn.ID, result.RunID, "", json.RawMessage(result.Output)))
	jobs, e := db.Actions().ListActions(ctx, u.ID, "job", 20, 0)
	must(t, e)
	if len(jobs) != 2 || workers.Load() != 4 {
		t.Fatal("missing course fanout", len(jobs), workers.Load())
	}
	x := actions.Executor{Repo: db.Actions(), Agents: repo}
	for range 2 {
		ran, e := x.RunOne(ctx)
		must(t, e)
		if !ran {
			t.Fatal("due notice not executed")
		}
	}
	notices, e := db.Actions().ListActions(ctx, u.ID, "notice", 20, 0)
	must(t, e)
	if len(notices) != 2 || chatRounds != 3 || workers.Load() != 4 {
		t.Fatal("scheduled Notice must not call model", len(notices), chatRounds, workers.Load())
	}
}
