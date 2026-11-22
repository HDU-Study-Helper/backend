package dbbridge

import (
	"bytes"
	"context"
	"encoding/json"

	"net/http/httptest"
	"strings"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/actions"
	"academic/backend/internal/agent"
	"academic/backend/internal/chatapi"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
)

type chatTestModel func(context.Context, f.ModelRequest) (f.ModelResponse, error)

func (m chatTestModel) Complete(c context.Context, q f.ModelRequest) (f.ModelResponse, error) {
	return m(c, q)
}
func TestChatTransportWorkerTodoQuestionAndIsolation(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	first := fixtureUser(t, db, "chat-user", model.RoleUser)
	foreign := fixtureUser(t, db, "another-user", model.RoleUser)
	must(t, db.EnableDefaultRoles(ctx))
	repo := db.Agents()
	agents, e := repo.ListAgents(ctx, first.ID, 10, "")
	must(t, e)
	roles := map[string]f.AgentInstance{}
	for _, a := range agents {
		a.Model = f.ModelConfig{Provider: a.Type, Model: "mock"}
		a.Limits.ContextBytes = 1 << 20
		a, e = repo.PutAgent(ctx, a)
		must(t, e)
		roles[a.Type] = a
	}
	wf, e := repo.SaveWorkflow(ctx, f.Scope{OwnerID: first.ID, AgentID: roles["learner"].ID}, f.Workflow{Name: "测试数分", Status: "active", Definition: json.RawMessage(`{"entity":{"id":"course-test","name":"测试数分","kind":"course"},"goal":"整理数分作业"}`)}, true)
	must(t, e)
	secret := "only-workflow-can-see-this"
	_, e = repo.SaveMemory(ctx, f.Scope{OwnerID: first.ID, AgentID: roles["worker"].ID, WorkflowID: wf.ID}, f.Memory{OwnerID: first.ID, WorkflowID: wf.ID, Visibility: "PRI", Title: "数分记忆", Content: secret, MemoryType: "BASE", Level: "L1", Status: "active"}, true)
	must(t, e)
	calls := 0
	workerCalls := 0
	tool := func(name, args string) f.ModelResponse {
		return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: f.NewID(), Type: "function", Function: f.FunctionCall{Name: name, Arguments: args}}}}, FinishReason: "tool_calls"}
	}
	finish := func(body string) f.ModelResponse {
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: body}, FinishReason: "stop"}
	}
	chat := chatTestModel(func(_ context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		calls++
		raw, _ := json.Marshal(q.Messages)
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("Workflow PRI leaked into Chat")
		}
		switch calls {
		case 1:
			return tool("workflow_list", `{}`), nil
		case 2:
			if !strings.Contains(q.Messages[len(q.Messages)-1].Content, wf.ID) {
				t.Fatal("missing owner workflow")
			}
			return tool("workflow_get", `{"workflow_id":"`+wf.ID+`"}`), nil
		case 3:
			args, _ := json.Marshal(f.AgentDispatchInput{Target: roles["worker"].ID, Task: "整理数分作业", Workflow: &f.WorkflowSelection{ID: wf.ID, Version: wf.Version}})
			return tool("agent_dispatch", string(args)), nil
		case 4:
			return finish(`{"text":"数分作业已记入待办。","cards":[],"interaction":null}`), nil
		case 5:
			return finish(`{"text":"选星期几？","cards":[],"interaction":{"type":"CHOICE","question":"选星期几？","options":[{"id":"fri","label":"周五"}]}}`), nil
		case 6:
			if !strings.Contains(string(raw), "周五") {
				t.Fatal("missing answer")
			}
			return finish(`{"text":"已收到周五的回答。","cards":[],"interaction":null}`), nil
		default:
			t.Fatal("unexpected repeated Chat call", calls)
			return f.ModelResponse{}, nil
		}
	})
	worker := chatTestModel(func(_ context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		workerCalls++
		raw, _ := json.Marshal(q.Messages)
		if !bytes.Contains(raw, []byte(secret)) {
			t.Fatal("bound Worker lacks private memory")
		}
		switch workerCalls {
		case 1:
			return tool("memory_list", `{"owner_workflow_id":"`+wf.ID+`"}`), nil
		case 2:
			return tool("public_todo_upsert", `{"idempotency_key":"task-1-create","todo_key":"class:test:assignment:1","title":"数分作业","evidence":["测试作业1"]}`), nil
		default:
			if !strings.Contains(q.Messages[len(q.Messages)-1].Content, `"ok":true`) {
				t.Fatal(q.Messages[len(q.Messages)-1])
			}
			return finish(`{"status":"COMPLETED","summary":"已记入待办","text":"数分作业已记入待办","artifacts":[],"interaction":null}`), nil
		}
	})
	service, e := agent.New(agent.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"worker": worker, "chat": chat}})
	must(t, e)
	must(t, actions.RegisterTools(service.Tools, db.Actions()))
	routes := (chatapi.API{Agent: service, Store: db.Chat(), Actions: db.Actions()}).Routes()
	request := func(user model.User, method, path, body string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user.ID != "" {
			r = r.WithContext(httpkit.WithUser(r.Context(), user))
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var v map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	request(model.User{}, "POST", "/threads", "", 401)
	th := request(first, "POST", "/threads", "", 201)["thread_id"].(string)
	base := "/threads/" + th
	request(foreign, "GET", base, "", 404)
	msg := `{"request_id":"one","message":"整理数分作业"}`
	v := request(first, "POST", base+"/messages", msg, 200)
	if v["state"] != "completed" || calls != 4 || workerCalls != 3 {
		t.Fatal(v, calls, workerCalls)
	}
	request(first, "POST", base+"/messages", msg, 200)
	if calls != 4 {
		t.Fatal("duplicate model run")
	}
	request(first, "POST", base+"/messages", `{"request_id":"one","message":"不同请求"}`, 409)
	if len(request(first, "GET", "/todos", "", 200)["items"].([]any)) != 1 || len(request(foreign, "GET", "/todos", "", 200)["items"].([]any)) != 0 {
		t.Fatal("public todo not scoped by owner")
	}
	v = request(first, "POST", base+"/messages", `{"request_id":"two","message":"讨论时间"}`, 200)
	id := v["output"].(map[string]any)["interaction"].(map[string]any)["question_id"].(string)
	answer, _ := json.Marshal(map[string]string{"request_id": "three", "question_id": id, "message": "周五"})
	request(first, "POST", base+"/answers", string(answer), 200)
	request(first, "POST", base+"/answers", string(answer), 200)
	if calls != 6 {
		t.Fatal("answer or initial action replayed", calls)
	}
	if len(request(first, "GET", base, "", 200)["turns"].([]any)) != 3 {
		t.Fatal("history not retained")
	}

}
