package dbbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	f "academic/agent/framework"
	agentservice "academic/backend/internal/agent"
	"academic/backend/internal/model"
)

func code(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || f.SafeError(err).Code != want {
		t.Fatalf("want %s got %v", want, err)
	}
}
func definition(name, provider string, tools, permissions []string) f.AgentDefinition {
	return f.AgentDefinition{Name: name, Type: "test-only", SystemPrompt: "Handle the supplied test task.", Model: f.ModelConfig{Provider: provider, Model: provider + "-model"}, EnabledTools: tools, Permissions: permissions}
}
func TestAgentRepositoryCRUDIsolationAndVersions(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "agent-owner", model.RoleUser)
	v := fixtureUser(t, db, "other-owner", model.RoleUser)
	repo := db.Agents()
	registry := f.Registry{Repository: repo}
	a, err := registry.Create(ctx, u.ID, definition("a", "mock", nil, nil))
	must(t, err)
	b, err := registry.Create(ctx, u.ID, definition("b", "mock", nil, nil))
	must(t, err)
	other, err := registry.Create(ctx, v.ID, definition("other", "mock", nil, nil))
	must(t, err)
	as := f.Scope{OwnerID: u.ID, AgentID: a.ID}
	bs := f.Scope{OwnerID: u.ID, AgentID: b.ID}
	_, err = repo.GetAgent(ctx, f.Scope{OwnerID: v.ID, AgentID: a.ID})
	code(t, err, "AGENT_NOT_FOUND")
	a.Name = "changed"
	a, err = registry.Save(ctx, u.ID, a)
	must(t, err)
	if a.Name != "changed" || a.CreatedAt.IsZero() {
		t.Fatal("agent update")
	}
	memory, err := repo.SaveMemory(ctx, as, f.Memory{Title: "remember", Content: "private", Priority: 1}, true)
	must(t, err)
	for _, scope := range []f.Scope{bs, {OwnerID: v.ID, AgentID: other.ID}} {
		_, err = repo.GetMemory(ctx, scope, memory.ID)
		code(t, err, "RESOURCE_NOT_FOUND")
		copy := memory
		copy.Content = "stolen"
		_, err = repo.SaveMemory(ctx, scope, copy, false)
		code(t, err, "RESOURCE_NOT_FOUND")
		err = repo.DeleteMemory(ctx, scope, memory.ID)
		code(t, err, "RESOURCE_NOT_FOUND")
	}
	memory.Content = "updated"
	memory, err = repo.SaveMemory(ctx, as, memory, false)
	must(t, err)
	found, err := repo.ListMemory(ctx, as, "UPDATED", 20)
	must(t, err)
	if len(found) != 1 || found[0].ID != memory.ID {
		t.Fatal("memory search")
	}
	must(t, repo.DeleteMemory(ctx, as, memory.ID))
	_, err = repo.GetMemory(ctx, as, memory.ID)
	code(t, err, "RESOURCE_NOT_FOUND")
	low, err := repo.SaveMemory(ctx, as, f.Memory{Title: "low", Content: "l", Priority: 0}, true)
	must(t, err)
	high, err := repo.SaveMemory(ctx, as, f.Memory{Title: "high", Content: "h", Priority: 10}, true)
	must(t, err)
	found, err = repo.ListMemory(ctx, as, "", 20)
	must(t, err)
	if found[0].ID != high.ID || found[1].ID != low.ID {
		t.Fatal("memory order")
	}
	w, err := repo.SaveWorkflow(ctx, as, f.Workflow{Name: "parent", Definition: json.RawMessage(`{"steps":[]}`)}, true)
	must(t, err)
	w.Name = "v2"
	w, err = repo.SaveWorkflow(ctx, bs, w, false)
	must(t, err)
	if w.Version != 2 || w.CreatedBy != a.ID {
		t.Fatal("workflow version or creator")
	}
	versions, err := repo.WorkflowVersions(ctx, u.ID, w.ID)
	must(t, err)
	if len(versions) != 2 || versions[0].Version != 1 || versions[0].Name != "parent" || versions[1].Name != "v2" {
		t.Fatal("version snapshots")
	}
	stale := w
	stale.Version = 1
	_, err = repo.SaveWorkflow(ctx, as, stale, false)
	code(t, err, "INVALID_ARGUMENT")
	_, err = repo.GetWorkflow(ctx, v.ID, w.ID)
	code(t, err, "RESOURCE_NOT_FOUND")
	_, err = repo.WorkflowVersions(ctx, v.ID, w.ID)
	code(t, err, "RESOURCE_NOT_FOUND")
	err = repo.DeleteWorkflow(ctx, v.ID, w.ID)
	code(t, err, "RESOURCE_NOT_FOUND")
	_, err = repo.SaveWorkflow(ctx, f.Scope{OwnerID: v.ID, AgentID: other.ID}, f.Workflow{Name: "foreign-child", ParentID: w.ID, Definition: json.RawMessage(`{}`)}, true)
	code(t, err, "PERMISSION_DENIED")
	child, err := repo.SaveWorkflow(ctx, as, f.Workflow{Name: "child", ParentID: w.ID, Definition: json.RawMessage(`{}`)}, true)
	must(t, err)
	changedParent := w
	changedParent.ParentID = child.ID
	_, err = repo.SaveWorkflow(ctx, as, changedParent, false)
	code(t, err, "INVALID_ARGUMENT")
	err = repo.DeleteWorkflow(ctx, u.ID, w.ID)
	code(t, err, "PERMISSION_DENIED")
	must(t, repo.DeleteWorkflow(ctx, u.ID, child.ID))
	must(t, repo.DeleteWorkflow(ctx, u.ID, w.ID))
	_, err = repo.WorkflowVersions(ctx, u.ID, w.ID)
	code(t, err, "RESOURCE_NOT_FOUND")
	must(t, repo.DeleteAgent(ctx, bs))
	_, err = repo.GetAgent(ctx, bs)
	code(t, err, "AGENT_NOT_FOUND")
	// Migration ledger is additive and idempotent.
	must(t, db.Migrate(ctx))
	var version int
	must(t, db.pool.QueryRow(ctx, `SELECT max(version) FROM dbbridge_migrations`).Scan(&version))
	if version != 5 {
		t.Fatal("migration version", version)
	}
}

type acceptanceModel struct {
	t        *testing.T
	role     string
	target   string
	mine     string
	other    string
	childRun string
}

func (m *acceptanceModel) Complete(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	m.t.Helper()
	if r.Config.Model != m.role+"-model" {
		m.t.Fatal("child inherited model configuration")
	}
	raw, _ := json.Marshal(r.Messages)
	if !strings.Contains(string(raw), m.mine) || strings.Contains(string(raw), m.other) {
		m.t.Fatal("memory injection/isolation")
	}
	responses := []f.Message{}
	for _, message := range r.Messages {
		if message.Role == "tool" {
			responses = append(responses, message)
			var result f.ToolResult
			must(m.t, json.Unmarshal([]byte(message.Content), &result))
			if !result.OK {
				m.t.Fatalf("tool failed: %s", message.Content)
			}
		}
	}
	makeCall := func(id, name string, args any) f.ToolCall {
		raw, _ := json.Marshal(args)
		return f.ToolCall{ID: id, Type: "function", Function: f.FunctionCall{Name: name, Arguments: string(raw)}}
	}
	toolReply := func(calls ...f.ToolCall) f.ModelResponse {
		return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}
	}
	if m.role == "B" {
		if len(r.Tools) != 1 || r.Tools[0].Name != "web_search" {
			m.t.Fatal("child inherited parent's permissions")
		}
		if len(responses) == 0 {
			return toolReply(makeCall("search", "web_search", map[string]any{"query": "public reference", "count": 1})), nil
		}
		if !strings.Contains(responses[0].Content, "https://example.com/reference") {
			m.t.Fatal("search provenance missing")
		}
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: `{"source":"https://example.com/reference","summary":"reference found"}`}, FinishReason: "stop"}, nil
	}
	switch len(responses) {
	case 0:
		return toolReply(makeCall("dispatch", "agent_dispatch", map[string]any{"target_agent_id": m.target, "task": "find a public reference"})), nil
	case 1:
		var result struct {
			Data f.RunResult `json:"data"`
		}
		must(m.t, json.Unmarshal([]byte(responses[0].Content), &result))
		m.childRun = result.Data.RunID
		if m.childRun == "" || !strings.Contains(result.Data.Output, "reference found") {
			m.t.Fatal("child result missing")
		}
		return toolReply(makeCall("workflow", "workflow_create", map[string]any{"name": "Reference workflow", "definition": map[string]any{"source": "https://example.com/reference"}}), makeCall("notify", "notification_send", map[string]any{"title": "Reference ready", "body": "A workflow definition was saved", "category": "test", "idempotency_key": "acceptance"})), nil
	case 3:
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "acceptance completed"}, FinishReason: "stop"}, nil
	}
	m.t.Fatal("unexpected acceptance state")
	return f.ModelResponse{}, nil
}

type acceptanceSearch struct{ calls int }

func (s *acceptanceSearch) Search(_ context.Context, q f.SearchRequest) ([]f.SearchResult, error) {
	s.calls++
	return []f.SearchResult{{Title: "Reference", URL: "https://example.com/reference", Summary: "Public reference"}}, nil
}
func (*acceptanceSearch) Fetch(context.Context, string) (f.Page, error) {
	return f.Page{}, f.Fail("NOT_IMPLEMENTED", "unused in demo")
}

type acceptanceNotifications struct {
	owner string
	calls int
}

func (n *acceptanceNotifications) Send(_ context.Context, s f.Scope, _ f.Notification) (f.NotificationReceipt, error) {
	n.owner = s.OwnerID
	n.calls++
	return f.NotificationReceipt{ID: "mock-request", Status: "accepted"}, nil
}
func TestFrameworkAcceptancePersistedBridgeDispatch(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "demo-user-a", model.RoleUser)
	v := fixtureUser(t, db, "demo-user-b", model.RoleUser)
	repo := db.Agents()
	registry := f.Registry{Repository: repo}
	a, err := registry.Create(ctx, u.ID, definition("Agent A", "A", []string{"agent_dispatch", "workflow_create", "notification_send"}, []string{"agent.dispatch", "workflow.write", "notification.send"}))
	must(t, err)
	b, err := registry.Create(ctx, u.ID, definition("Agent B", "B", []string{"web_search", "notification_send"}, []string{"web.search"}))
	must(t, err)
	am := &acceptanceModel{t: t, role: "A", target: b.ID, mine: "MEMORY_A_ONLY", other: "MEMORY_B_ONLY"}
	bm := &acceptanceModel{t: t, role: "B", mine: "MEMORY_B_ONLY", other: "MEMORY_A_ONLY"}
	search := &acceptanceSearch{}
	notifications := &acceptanceNotifications{}
	_, err = repo.SaveMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: a.ID}, f.Memory{Title: "A", Content: am.mine}, true)
	must(t, err)
	_, err = repo.SaveMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: b.ID}, f.Memory{Title: "B", Content: bm.mine}, true)
	must(t, err)
	service, err := agentservice.New(agentservice.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"A": am, "B": bm}, Search: search, Backend: agentservice.BackendServices{Notifications: notifications, Data: db.AgentData()}})
	must(t, err)
	for range 2 {
		result, err := service.Run(ctx, u.ID, f.RunRequest{AgentID: a.ID, Trigger: f.SystemEvent, Input: json.RawMessage(`{"task":"acceptance"}`)})
		must(t, err)
		if result.Output != "acceptance completed" {
			t.Fatal(result)
		}
		run, err := repo.GetRun(ctx, u.ID, result.RunID)
		must(t, err)
		child, err := repo.GetRun(ctx, u.ID, am.childRun)
		must(t, err)
		if run.ModelCalls != 3 || run.ToolCalls != 3 || run.Status != "succeeded" || child.ParentRunID != run.ID || child.DispatcherID != a.ID || child.AgentID != b.ID || child.Status != "succeeded" {
			t.Fatal("parent/child run persistence")
		}
		_, err = repo.GetRun(ctx, v.ID, result.RunID)
		code(t, err, "RESOURCE_NOT_FOUND")
	}
	if search.calls != 2 || notifications.calls != 2 || notifications.owner != u.ID {
		t.Fatal("bridge service calls missing")
	}
	for table, want := range map[string]int{"agent_memories": 2, "agent_workflows": 2, "agent_workflow_versions": 2, "agent_runs": 4} {
		var count int
		must(t, db.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE owner_user_id=$1`, u.ID).Scan(&count))
		if count != want {
			t.Fatalf("%s count %d want %d", table, count, want)
		}
	}
	_, err = service.Run(ctx, v.ID, f.RunRequest{AgentID: a.ID, Trigger: f.SystemEvent, Input: json.RawMessage(`{}`)})
	code(t, err, "AGENT_NOT_FOUND")
	t.Log("persisted acceptance: two isolated memories, A → Tool Registry → Bridge → B → mock Search; workflow and notification request; owner isolation; no agent HTTP")
}
func TestAgentDataReadonlyWhitelistAndOwnerScope(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "data-a", model.RoleUser)
	v := fixtureUser(t, db, "data-b", model.RoleUser)
	for _, owner := range []string{u.ID, v.ID} {
		_, err := db.pool.Exec(ctx, `INSERT INTO platform_connections(user_id,id,source,state) VALUES($1,'c','mooc','active');`, owner)
		must(t, err)
		_, err = db.pool.Exec(ctx, `INSERT INTO platform_records(user_id,connection_id,source,kind,external_id,parent_id,hash,content,observed_at) VALUES($1,'c','mooc','course','one','','hash',$2,now())`, owner, `{"title":"`+owner+`","text":"owned","session":"never expose"}`)
		must(t, err)
	}
	data := db.AgentData()
	catalog, err := data.Catalog(ctx, f.Scope{OwnerID: u.ID})
	must(t, err)
	if len(catalog) != 1 || catalog[0].Name != "platform_records" {
		t.Fatal("catalog")
	}
	q := f.Query{Dataset: "platform_records", Fields: []string{"title", "text"}, Limit: 1}
	result, err := data.Query(ctx, f.Scope{OwnerID: u.ID}, q)
	must(t, err)
	if len(result.Rows) != 1 || result.Rows[0]["title"] != u.ID {
		t.Fatal("cross-owner query")
	}
	q.Filters = []f.Filter{{Field: "title", Operator: "eq", Value: v.ID}}
	result, err = data.Query(ctx, f.Scope{OwnerID: u.ID}, q)
	must(t, err)
	if len(result.Rows) != 0 {
		t.Fatal("filter escaped owner")
	}
	q.Fields = []string{"session"}
	_, err = data.Query(ctx, f.Scope{OwnerID: u.ID}, q)
	code(t, err, "PERMISSION_DENIED")
	q.Dataset = "agent_memories"
	_, err = data.Query(ctx, f.Scope{OwnerID: u.ID}, q)
	code(t, err, "PERMISSION_DENIED")
	q = f.Query{Dataset: "platform_records", Fields: []string{"title"}, Limit: 101}
	_, err = data.Query(ctx, f.Scope{OwnerID: u.ID}, q)
	code(t, err, "PERMISSION_DENIED")
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	q.Limit = 1
	_, err = data.Query(expired, f.Scope{OwnerID: u.ID}, q)
	code(t, err, "TIMEOUT")
}
