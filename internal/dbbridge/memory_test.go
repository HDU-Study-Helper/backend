package dbbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	f "academic/agent/framework"
	agentservice "academic/backend/internal/agent"
	"academic/backend/internal/bridge"
	"academic/backend/internal/model"
)

type memoryModel struct {
	next func(f.ModelRequest) f.ModelResponse
}

func (m *memoryModel) Complete(_ context.Context, req f.ModelRequest) (f.ModelResponse, error) {
	return m.next(req), nil
}

// Every memory operation below travels through the production Runtime, tool
// schema validation, permissions and PostgreSQL repository, not a fake memory map.
func TestMemoryV02RuntimeIsolationRelationsAndValidity(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Agents()
	registry := f.Registry{Repository: repo}
	u := fixtureUser(t, db, "memory-owner", model.RoleUser)
	v := fixtureUser(t, db, "memory-other", model.RoleUser)
	tools := []string{"memory_create", "memory_get", "memory_search", "memory_list", "memory_update", "memory_delete", "memory_create_relation", "memory_get_relations"}
	createAgent := func(owner, name string) f.AgentInstance {
		a, e := registry.Create(ctx, owner, definition(name, "memory-mock", tools, []string{"memory.read", "memory.write"}))
		must(t, e)
		return a
	}
	a, b, learner, other := createAgent(u.ID, "Filter"), createAgent(u.ID, "Worker"), createAgent(u.ID, "Learner"), createAgent(v.ID, "Other")
	as := f.Scope{OwnerID: u.ID, AgentID: a.ID}
	w, e := repo.SaveWorkflow(ctx, as, f.Workflow{Name: "Calculus", Definition: json.RawMessage(`{}`)}, true)
	must(t, e)
	w2, e := repo.SaveWorkflow(ctx, as, f.Workflow{Name: "English", Definition: json.RawMessage(`{}`)}, true)
	must(t, e)
	wf := f.WorkflowContext(ctx, u.ID, w.ID)
	wf2 := f.WorkflowContext(ctx, u.ID, w2.ID)
	model := &memoryModel{}
	service, e := agentservice.New(agentservice.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"memory-mock": model}})
	must(t, e)
	count := 0
	runTool := func(runCtx context.Context, agent f.AgentInstance, name string, args any, want string) json.RawMessage {
		t.Helper()
		count++
		round := 0
		var payload json.RawMessage
		raw, _ := json.Marshal(args)
		model.next = func(req f.ModelRequest) f.ModelResponse {
			round++
			if round == 1 {
				return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "memory-op", Type: "function", Function: f.FunctionCall{Name: name, Arguments: string(raw)}}}}, FinishReason: "tool_calls"}
			}
			if round != 2 {
				t.Fatal("unexpected model round")
			}
			var result struct {
				OK    bool            `json:"ok"`
				Data  json.RawMessage `json:"data"`
				Error *f.Error        `json:"error"`
			}
			must(t, json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &result))
			if want == "" {
				if !result.OK || result.Error != nil {
					t.Fatalf("%s failed: %+v", name, result.Error)
				}
			} else if result.Error == nil || result.Error.Code != want {
				t.Fatalf("%s want %s got %+v", name, want, result)
			}
			payload = result.Data
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "verified"}, FinishReason: "stop"}
		}
		result, err := service.Run(runCtx, agent.OwnerID, f.RunRequest{AgentID: agent.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{"workflow_id":"forged","owner_user_id":"forged"}`)})
		must(t, err)
		record, err := repo.GetRun(ctx, agent.OwnerID, result.RunID)
		must(t, err)
		if record.ToolCalls != 1 || len(record.ToolExecutions) != 1 {
			t.Fatal("tool execution was not persisted")
		}
		if runCtx == wf && record.WorkflowID != w.ID {
			t.Fatal("workflow execution identity not persisted")
		}
		return payload
	}
	memory := func(raw json.RawMessage) f.Memory { var m f.Memory; must(t, json.Unmarshal(raw, &m)); return m }
	create := func(runCtx context.Context, agent f.AgentInstance, args map[string]any) f.Memory {
		return memory(runTool(runCtx, agent, "memory_create", args, ""))
	}
	getArgs := func(id string) map[string]any { return map[string]any{"memory_id": id} }
	pub := create(ctx, a, map[string]any{"title": "数分课程", "content": "SHARED_CONFIRMED_COURSE", "visibility": "PUB", "memory_type": "BASE", "level": "L1", "structured_content": map[string]any{"course": "calculus"}, "source_refs": []any{map[string]any{"source": "confirmed"}}})
	for _, agent := range []f.AgentInstance{a, b, learner} {
		runTool(ctx, agent, "memory_get", getArgs(pub.ID), "")
	}
	runTool(ctx, other, "memory_get", getArgs(pub.ID), "RESOURCE_NOT_FOUND")
	private := create(ctx, a, map[string]any{"title": "Filter route", "content": "FILTER_PRIVATE_ONLY"})
	runTool(ctx, b, "memory_get", getArgs(private.ID), "RESOURCE_NOT_FOUND")
	runTool(ctx, b, "memory_update", map[string]any{"memory_id": private.ID, "visibility": "PRI", "owner_agent_id": b.ID}, "RESOURCE_NOT_FOUND")
	runTool(ctx, b, "memory_delete", getArgs(private.ID), "RESOURCE_NOT_FOUND")
	workflow := create(wf, a, map[string]any{"title": "数分规则", "content": "CALCULUS_WORKFLOW_ONLY", "visibility": "PRI", "owner_workflow_id": w.ID, "level": "L2", "parent_memory_id": pub.ID})
	runTool(wf, b, "memory_get", getArgs(workflow.ID), "")
	runTool(wf2, b, "memory_get", getArgs(workflow.ID), "RESOURCE_NOT_FOUND")
	runTool(ctx, a, "memory_get", getArgs(workflow.ID), "RESOURCE_NOT_FOUND")
	runTool(ctx, a, "memory_create", map[string]any{"title": "forged", "content": "no", "visibility": "PRI", "owner_workflow_id": w.ID}, "PERMISSION_DENIED")
	runTool(wf, a, "memory_create", map[string]any{"title": "two owners", "content": "no", "visibility": "PRI", "owner_agent_id": a.ID, "owner_workflow_id": w.ID}, "INVALID_ARGUMENT")
	runTool(ctx, a, "memory_create", map[string]any{"title": "forged tenant", "content": "no", "owner_user_id": v.ID}, "INVALID_ARGUMENT")
	runTool(ctx, a, "memory_search", map[string]any{"owner_agent_id": b.ID}, "PERMISSION_DENIED")
	runTool(wf, a, "memory_search", map[string]any{"owner_workflow_id": w2.ID}, "PERMISSION_DENIED")
	rows := runTool(wf, b, "memory_search", map[string]any{"query": "数分", "level": "L2", "memory_type": "BASE", "parent_memory_id": pub.ID}, "")
	if !strings.Contains(string(rows), workflow.ID) {
		t.Fatal("keyword/level/parent search missed workflow memory")
	}
	relation := map[string]any{"source_memory_id": pub.ID, "target_memory_id": private.ID, "relation_type": "ROUTES_TO"}
	runTool(ctx, a, "memory_create_relation", relation, "")
	runTool(ctx, a, "memory_create_relation", relation, "INVALID_ARGUMENT")
	runTool(ctx, b, "memory_create_relation", relation, "RESOURCE_NOT_FOUND")
	relations := runTool(ctx, b, "memory_get_relations", map[string]any{"memory_id": pub.ID}, "")
	if string(relations) != "[]" {
		t.Fatal("relation leaked private endpoint to another agent")
	}
	relations = runTool(ctx, a, "memory_get_relations", map[string]any{"memory_id": pub.ID, "direction": "outgoing", "relation_type": "ROUTES_TO", "depth": 1}, "")
	if !strings.Contains(string(relations), private.ID) {
		t.Fatal("direct association lookup failed")
	}
	runTool(ctx, a, "memory_get_relations", map[string]any{"memory_id": pub.ID, "depth": 2}, "INVALID_ARGUMENT")
	otherPrivate := create(ctx, other, map[string]any{"title": "other user", "content": "OTHER_USER_SECRET"})
	runTool(ctx, a, "memory_create_relation", map[string]any{"source_memory_id": pub.ID, "target_memory_id": otherPrivate.ID, "relation_type": "RELATED_TO"}, "RESOURCE_NOT_FOUND")
	runTool(ctx, a, "memory_create_relation", map[string]any{"source_memory_id": pub.ID, "target_memory_id": "missing", "relation_type": "RELATED_TO"}, "RESOURCE_NOT_FOUND")
	runTool(ctx, a, "memory_update", map[string]any{"memory_id": private.ID, "owner_agent_id": b.ID}, "PERMISSION_DENIED")
	// Ownership promotion validates the source ACL and requires clearing its owner.
	promoted := create(ctx, a, map[string]any{"title": "publish", "content": "PUBLISHED_FACT"})
	runTool(ctx, a, "memory_update", map[string]any{"memory_id": promoted.ID, "visibility": "PUB", "owner_agent_id": nil}, "")
	runTool(ctx, b, "memory_get", getArgs(promoted.ID), "")
	runTool(ctx, a, "memory_update", map[string]any{"memory_id": promoted.ID, "visibility": "PRI", "owner_workflow_id": w.ID}, "PERMISSION_DENIED")
	runTool(wf, a, "memory_update", map[string]any{"memory_id": promoted.ID, "visibility": "PRI", "owner_workflow_id": w.ID}, "")
	runTool(ctx, b, "memory_get", getArgs(promoted.ID), "RESOURCE_NOT_FOUND")
	// TEMP+PERSONAL is independent of stability, and explicit history remains readable.
	expired := create(ctx, a, map[string]any{"title": "expired", "content": "EXPIRED_NOT_CURRENT", "visibility": "PUB", "memory_type": "TEMP", "is_personal": true, "valid_until": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)})
	future := create(ctx, a, map[string]any{"title": "future", "content": "FUTURE_NOT_CURRENT", "visibility": "PUB", "valid_from": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	archived := create(ctx, a, map[string]any{"title": "archived", "content": "ARCHIVED_NOT_CURRENT", "visibility": "PUB", "status": "archived"})
	raw := runTool(ctx, a, "memory_search", map[string]any{}, "")
	for _, m := range []f.Memory{expired, future, archived} {
		if strings.Contains(string(raw), m.ID) {
			t.Fatal("inactive fact in default search")
		}
	}
	raw = runTool(ctx, a, "memory_search", map[string]any{"validity": "expired", "memory_type": "TEMP", "is_personal": true}, "")
	if !strings.Contains(string(raw), expired.ID) {
		t.Fatal("expired history unavailable")
	}
	runTool(ctx, a, "memory_get", getArgs(expired.ID), "")
	assertAuto := func(runCtx context.Context, agent f.AgentInstance, want, absent []string) {
		t.Helper()
		model.next = func(req f.ModelRequest) f.ModelResponse {
			encoded, _ := json.Marshal(req.Messages)
			for _, v := range want {
				if !strings.Contains(string(encoded), v) {
					t.Fatalf("autoload missing %s", v)
				}
			}
			for _, v := range absent {
				if strings.Contains(string(encoded), v) {
					t.Fatalf("autoload leaked %s", v)
				}
			}
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}
		}
		_, err := service.Run(runCtx, agent.OwnerID, f.RunRequest{AgentID: agent.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
		must(t, err)
	}
	assertAuto(ctx, a, []string{"SHARED_CONFIRMED_COURSE", "FILTER_PRIVATE_ONLY"}, []string{"CALCULUS_WORKFLOW_ONLY", "EXPIRED_NOT_CURRENT", "FUTURE_NOT_CURRENT", "ARCHIVED_NOT_CURRENT", "OTHER_USER_SECRET"})
	assertAuto(wf, b, []string{"SHARED_CONFIRMED_COURSE", "CALCULUS_WORKFLOW_ONLY"}, []string{"FILTER_PRIVATE_ONLY", "EXPIRED_NOT_CURRENT", "FUTURE_NOT_CURRENT", "ARCHIVED_NOT_CURRENT", "OTHER_USER_SECRET"})
	assertAuto(wf2, b, []string{"SHARED_CONFIRMED_COURSE"}, []string{"FILTER_PRIVATE_ONLY", "CALCULUS_WORKFLOW_ONLY", "PUBLISHED_FACT"})
	assertAuto(ctx, learner, []string{"SHARED_CONFIRMED_COURSE"}, []string{"FILTER_PRIVATE_ONLY", "CALCULUS_WORKFLOW_ONLY"})
	runTool(ctx, a, "memory_delete", getArgs(private.ID), "")
	if string(runTool(ctx, a, "memory_get_relations", getArgs(pub.ID), "")) != "[]" {
		t.Fatal("delete did not cascade relation")
	}
	// Parent cannot be hidden from existing PUB children or moved below them.
	child := create(ctx, a, map[string]any{"title": "type", "content": "child", "visibility": "PUB", "level": "L2", "parent_memory_id": pub.ID})
	runTool(ctx, a, "memory_update", map[string]any{"memory_id": pub.ID, "visibility": "PRI", "owner_agent_id": a.ID}, "INVALID_ARGUMENT")
	runTool(ctx, a, "memory_update", map[string]any{"memory_id": pub.ID, "level": "L3"}, "INVALID_ARGUMENT")
	runTool(ctx, a, "memory_delete", getArgs(pub.ID), "")
	if memory(runTool(ctx, a, "memory_get", getArgs(child.ID), "")).ParentID != "" {
		t.Fatal("deleted parent was not detached")
	}
	t.Logf("verified %d real Runtime tool calls across Filter/Worker/Learner, two users, two Workflow contexts", count)
}

func TestMemoryV02MigrationPreservesLegacy(t *testing.T) {
	db := testDBVersion(t, 8)
	ctx := context.Background()
	u := fixtureUser(t, db, "legacy-memory", model.RoleUser)
	a, e := (f.Registry{Repository: db.Agents()}).Create(ctx, u.ID, definition("legacy", "mock", nil, nil))
	must(t, e)
	// The test helper explicitly stops at V8 to exercise an actual pre-upgrade row.
	_, e = db.pool.Exec(ctx, `INSERT INTO agent_memories(memory_id,owner_user_id,agent_id,title,content,category,priority,created_at,updated_at) VALUES('legacy-id',$1,$2,'old title','old content','routing',7,'2025-01-01','2025-01-02')`, u.ID, a.ID)
	must(t, e)
	must(t, db.Migrate(ctx))
	must(t, db.Migrate(ctx))
	m, e := db.Agents().GetMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: a.ID}, "legacy-id")
	must(t, e)
	if m.Visibility != "PRI" || m.AgentID != a.ID || m.WorkflowID != "" || m.MemoryType != "BASE" || m.Level != "L1" || m.Content != "old content" || m.Category != "routing" || m.Priority != 7 || m.CreatedAt.Year() != 2025 || m.UpdatedAt.Day() != 2 {
		t.Fatalf("unsafe migration: %+v", m)
	}
	var original string
	must(t, db.pool.QueryRow(ctx, `SELECT content FROM agent_memories WHERE memory_id='legacy-id'`).Scan(&original))
	if original != m.Content {
		t.Fatal("legacy data destroyed")
	}
	var n int
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM memories WHERE memory_id='legacy-id'`).Scan(&n))
	if n != 1 {
		t.Fatal("migration duplicated memory")
	}
}

func TestMemoryV02DatabaseConstraints(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Agents()
	u := fixtureUser(t, db, "db-memory-a", model.RoleUser)
	v := fixtureUser(t, db, "db-memory-b", model.RoleUser)
	a, e := (f.Registry{Repository: repo}).Create(ctx, u.ID, definition("a", "mock", nil, nil))
	must(t, e)
	b, e := (f.Registry{Repository: repo}).Create(ctx, v.ID, definition("b", "mock", nil, nil))
	must(t, e)
	own, e := repo.SaveMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: a.ID}, f.Memory{Title: "own", Content: "own", Visibility: "PUB"}, true)
	must(t, e)
	foreign, e := repo.SaveMemory(ctx, f.Scope{OwnerID: v.ID, AgentID: b.ID}, f.Memory{Title: "foreign", Content: "foreign", Visibility: "PUB"}, true)
	must(t, e)
	checks := []string{
		fmt.Sprintf(`UPDATE memories SET owner_agent_id='%s' WHERE memory_id='%s'`, a.ID, own.ID),
		fmt.Sprintf(`UPDATE memories SET visibility='PRI' WHERE memory_id='%s'`, own.ID),
		fmt.Sprintf(`UPDATE memories SET parent_memory_id='%s' WHERE memory_id='%s'`, foreign.ID, own.ID),
		fmt.Sprintf(`INSERT INTO memory_relations(relation_id,owner_user_id,source_memory_id,target_memory_id,relation_type,created_by_agent_id) VALUES('bad','%s','%s','%s','RELATED_TO','%s')`, u.ID, own.ID, foreign.ID, a.ID),
	}
	for _, sql := range checks {
		if _, e = db.pool.Exec(ctx, sql); e == nil {
			t.Fatal("database accepted invalid owner/reference")
		}
	}
	// An active owner disappearing during a run cannot keep reading memories.
	_, e = db.Users().Update(ctx, u.ID, model.UserUpdate{Disabled: func() *bool { x := true; return &x }()})
	must(t, e)
	_, e = repo.GetMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: a.ID}, own.ID)
	code(t, e, "RESOURCE_NOT_FOUND")
}

func TestMemoryV02AssociativeRetrievalThroughBridge(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Agents()
	u := fixtureUser(t, db, "associative-owner", model.RoleUser)
	a, e := (f.Registry{Repository: repo}).Create(ctx, u.ID, definition("Filter", "memory-mock", []string{"memory_search", "memory_get", "memory_get_relations", "memory_create_relation"}, []string{"memory.read", "memory.write"}))
	must(t, e)
	scope := f.Scope{OwnerID: u.ID, AgentID: a.ID}
	course, e := repo.SaveMemory(ctx, scope, f.Memory{Title: "数分", Content: "确认的课程", Visibility: "PUB", Priority: -10}, true)
	must(t, e)
	route, e := repo.SaveMemory(ctx, scope, f.Memory{Title: "route", Content: "ROUTE_TO_CALCULUS_WORKFLOW", Priority: -10}, true)
	must(t, e)
	// Force retrieval rather than letting automatic context injection answer it.
	a.Limits.MemoryBytes = 1
	a, e = (f.Registry{Repository: repo}).Save(ctx, u.ID, a)
	must(t, e)
	round := 0
	model := &memoryModel{}
	request := func(name string, args any) f.ModelResponse {
		raw, _ := json.Marshal(args)
		return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: fmt.Sprint(round), Type: "function", Function: f.FunctionCall{Name: name, Arguments: string(raw)}}}}, FinishReason: "tool_calls"}
	}
	last := func(req f.ModelRequest) json.RawMessage {
		var result struct {
			OK    bool            `json:"ok"`
			Data  json.RawMessage `json:"data"`
			Error *f.Error        `json:"error"`
		}
		must(t, json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &result))
		if !result.OK {
			t.Fatalf("chain tool failed: %+v", result.Error)
		}
		return result.Data
	}
	model.next = func(req f.ModelRequest) f.ModelResponse {
		round++
		switch round {
		case 1:
			for _, message := range req.Messages {
				if strings.Contains(message.Content, route.Content) {
					t.Fatal("retrieval answer was auto-injected despite budget")
				}
			}
			return request("memory_search", map[string]any{"query": "数分"})
		case 2:
			var rows []f.Memory
			must(t, json.Unmarshal(last(req), &rows))
			if len(rows) != 1 || rows[0].ID != course.ID {
				t.Fatal("search did not return course")
			}
			return request("memory_get", map[string]any{"memory_id": rows[0].ID})
		case 3:
			_ = last(req)
			return request("memory_create_relation", map[string]any{"source_memory_id": course.ID, "target_memory_id": route.ID, "relation_type": "ROUTES_TO"})
		case 4:
			_ = last(req)
			return request("memory_get_relations", map[string]any{"memory_id": course.ID, "direction": "outgoing"})
		case 5:
			var links []f.MemoryRelation
			must(t, json.Unmarshal(last(req), &links))
			if len(links) != 1 {
				t.Fatal("missing route relation")
			}
			return request("memory_get", map[string]any{"memory_id": links[0].TargetID})
		case 6:
			if !strings.Contains(string(last(req)), route.Content) {
				t.Fatal("route content unavailable")
			}
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "route verified from memory evidence"}, FinishReason: "stop"}
		default:
			t.Fatal("unexpected chain round")
			return f.ModelResponse{}
		}
	}
	service, e := agentservice.New(agentservice.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"memory-mock": model}})
	must(t, e)
	payload, _ := json.Marshal(f.RunRequest{AgentID: a.ID, Trigger: f.DataChanged, Input: json.RawMessage(`{"text":"数分第三次作业延期"}`)})
	reply, e := service.Call(ctx, bridge.Request{Version: bridge.Version, OwnerID: u.ID, Operation: agentservice.RunOperation, Payload: payload})
	must(t, e)
	var result f.RunResult
	must(t, json.Unmarshal(reply.Payload, &result))
	record, e := repo.GetRun(ctx, u.ID, result.RunID)
	must(t, e)
	if record.ToolCalls != 5 || record.ModelCalls != 6 || !strings.Contains(result.Output, "verified") {
		t.Fatal("incomplete associative retrieval")
	}
}

func TestMemoryV02TrustedWorkflowAndBudget(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Agents()
	u := fixtureUser(t, db, "wf-trust", model.RoleUser)
	other := fixtureUser(t, db, "wf-trust-other", model.RoleUser)
	a, e := (f.Registry{Repository: repo}).Create(ctx, u.ID, definition("a", "mock", nil, nil))
	must(t, e)
	w, e := repo.SaveWorkflow(ctx, f.Scope{OwnerID: u.ID, AgentID: a.ID}, f.Workflow{Name: "owned", Definition: json.RawMessage(`{}`)}, true)
	must(t, e)
	m, e := repo.SaveMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: a.ID, WorkflowID: w.ID}, f.Memory{Visibility: "PRI", WorkflowID: w.ID, Title: "workflow", Content: "WF_CONTEXT_SECRET"}, true)
	must(t, e)
	model := &memoryModel{next: func(req f.ModelRequest) f.ModelResponse {
		raw, _ := json.Marshal(req.Messages)
		if strings.Contains(string(raw), m.Content) {
			t.Fatal("JSON forged Workflow context")
		}
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}
	}}
	service, e := agentservice.New(agentservice.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"mock": model}})
	must(t, e)
	req := f.RunRequest{AgentID: a.ID, Trigger: f.WorkflowEvent, Input: json.RawMessage(fmt.Sprintf(`{"workflow_id":"%s"}`, w.ID))}
	_, e = service.Run(ctx, u.ID, req)
	must(t, e)
	_, e = service.Run(f.WorkflowContext(ctx, other.ID, w.ID), u.ID, req)
	code(t, e, "PERMISSION_DENIED")
	_, e = service.Run(f.WorkflowContext(ctx, u.ID, "nonexistent"), u.ID, req)
	code(t, e, "RESOURCE_NOT_FOUND")
}

func TestMemoryV02DispatchPreservesTrustedWorkflow(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	repo := db.Agents()
	u := fixtureUser(t, db, "dispatch-memory", model.RoleUser)
	registry := f.Registry{Repository: repo}
	a, e := registry.Create(ctx, u.ID, definition("Filter", "mock", []string{"agent_dispatch"}, []string{"agent.dispatch"}))
	must(t, e)
	b, e := registry.Create(ctx, u.ID, definition("Worker", "mock", nil, nil))
	must(t, e)
	a.Model.Model = "parent"
	_, e = registry.Save(ctx, u.ID, a)
	must(t, e)
	b.Model.Model = "child"
	_, e = registry.Save(ctx, u.ID, b)
	must(t, e)
	scope := f.Scope{OwnerID: u.ID, AgentID: a.ID}
	w, e := repo.SaveWorkflow(ctx, scope, f.Workflow{Name: "context", Definition: json.RawMessage(`{}`)}, true)
	must(t, e)
	scope.WorkflowID = w.ID
	_, e = repo.SaveMemory(ctx, scope, f.Memory{Title: "wf", Content: "DISPATCH_WORKFLOW", WorkflowID: w.ID}, true)
	must(t, e)
	_, e = repo.SaveMemory(ctx, scope, f.Memory{Title: "private", Content: "PARENT_PRIVATE"}, true)
	must(t, e)
	childCalled := false
	parentRound := 0
	model := &memoryModel{}
	model.next = func(req f.ModelRequest) f.ModelResponse {
		if req.Config.Model == "child" {
			raw, _ := json.Marshal(req.Messages)
			if !strings.Contains(string(raw), "DISPATCH_WORKFLOW") || strings.Contains(string(raw), "PARENT_PRIVATE") {
				t.Fatal("dispatch workflow or agent isolation failed")
			}
			childCalled = true
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "child verified"}, FinishReason: "stop"}
		}
		parentRound++
		if parentRound == 1 {
			args, _ := json.Marshal(map[string]any{"target_agent_id": b.ID, "task": "verify context", "context": map[string]any{"workflow_id": "forged"}})
			return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "dispatch", Type: "function", Function: f.FunctionCall{Name: "agent_dispatch", Arguments: string(args)}}}}, FinishReason: "tool_calls"}
		}
		if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "child verified") {
			t.Fatal("child result not returned")
		}
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}
	}
	service, e := agentservice.New(agentservice.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"mock": model}})
	must(t, e)
	_, e = service.Run(f.WorkflowContext(ctx, u.ID, w.ID), u.ID, f.RunRequest{AgentID: a.ID, Trigger: f.WorkflowEvent, Input: json.RawMessage(`{}`)})
	must(t, e)
	if !childCalled {
		t.Fatal("child did not run")
	}
}

// Read-only live migration audit. It never migrates or modifies the database,
// never outputs memory contents, identities, credentials or connection strings.
func TestMemoryV02LiveMigrationAudit(t *testing.T) {
	if os.Getenv("MEMORY_V02_LIVE_AUDIT") != "1" {
		t.Skip("explicit live audit only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal("live audit database unavailable")
	}
	defer db.Close()
	var version, legacy int
	must(t, db.pool.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM dbbridge_migrations`).Scan(&version))
	must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM agent_memories`).Scan(&legacy))
	report := map[string]int{"schema_version": version, "legacy_memories": legacy}
	if version >= 9 {
		var total, matches int
		must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM memories`).Scan(&total))
		must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM agent_memories a JOIN memories m ON m.memory_id=a.memory_id AND m.owner_user_id=a.owner_user_id AND m.owner_agent_id=a.agent_id AND m.visibility='PRI' AND m.title=a.title AND m.content=a.content AND m.category=a.category AND m.priority=a.priority AND m.created_at=a.created_at AND m.updated_at=a.updated_at`).Scan(&matches))
		report["current_memories"] = total
		report["unchanged_legacy_copies"] = matches
	}
	raw, _ := json.Marshal(report)
	t.Log(string(raw))
}
