package dbbridge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	f "academic/agent/framework"
	agentservice "academic/backend/internal/agent"
	"academic/backend/internal/model"
)

func TestUnifiedWorkerDispatchPersisted(t *testing.T) {
	call := func(id, name string, args any) f.ModelResponse {
		raw, err := json.Marshal(args)
		must(t, err)
		return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: id, Type: "function", Function: f.FunctionCall{Name: name, Arguments: string(raw)}}}}, FinishReason: "tool_calls"}
	}
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "unified-dispatch", model.RoleUser)
	repo := db.Agents()
	registry := f.Registry{Repository: repo}
	parent, err := registry.Create(ctx, u.ID, definition("Filter test", "filter-test", []string{"agent_dispatch", "memory_get"}, []string{"agent.dispatch", "workflow.read", "memory.read"}))
	must(t, err)
	child, err := registry.Create(ctx, u.ID, definition("Worker test", "worker-test", nil, []string{"memory.read"}))
	must(t, err)
	w, err := repo.SaveWorkflow(ctx, f.Scope{OwnerID: u.ID, AgentID: parent.ID}, f.Workflow{Name: "dispatch course", Status: "active", Definition: json.RawMessage(`{"goal":"handle authorized event","entity":{"kind":"course","id":"test-course/class","name":"测试高等数学"}}`)}, true)
	must(t, err)
	private, err := repo.SaveMemory(ctx, f.Scope{OwnerID: u.ID, AgentID: child.ID, WorkflowID: w.ID}, f.Memory{Title: "workflow private", Content: "WORKFLOW_PRIVATE_SENTINEL", Visibility: "PRI", WorkflowID: w.ID}, true)
	must(t, err)
	steps := 0
	var childRunID string
	pm := &memoryModel{next: func(req f.ModelRequest) f.ModelResponse {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "WORKFLOW_PRIVATE_SENTINEL") {
				t.Fatal("child memory leaked to parent")
			}
		}
		switch steps {
		case 0:
			steps++
			args := f.AgentDispatchInput{Target: child.ID, Task: "read only event", Workflow: &f.WorkflowSelection{ID: w.ID, Version: w.Version}, Context: json.RawMessage(`{"event":{"event_id":"original-event","content":"Original unchanged content","source":{"platform":"manual"},"evidence_refs":[]}}`)}
			return call("dispatch", "agent_dispatch", args)
		case 1:
			steps++
			var tr struct {
				OK   bool        `json:"ok"`
				Data f.RunResult `json:"data"`
			}
			must(t, json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &tr))
			if !tr.OK || tr.Data.Output != "worker completed" {
				t.Fatal("child result not returned", req.Messages[len(req.Messages)-1])
			}
			childRunID = tr.Data.RunID
			return call("private", "memory_get", map[string]any{"memory_id": private.ID})
		default:
			var tr f.ToolResult
			must(t, json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &tr))
			if tr.OK || tr.Error == nil || tr.Error.Code != "RESOURCE_NOT_FOUND" {
				t.Fatal("parent acquired child workflow memory", tr)
			}
			return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "parent completed"}, FinishReason: "stop"}
		}
	}}
	cm := &memoryModel{next: func(req f.ModelRequest) f.ModelResponse {
		all := ""
		for _, msg := range req.Messages {
			all += msg.Content
		}
		if !strings.Contains(all, "WORKFLOW_PRIVATE_SENTINEL") || !strings.Contains(all, "Original unchanged content") || !strings.Contains(req.Messages[0].Content, "workflow_id="+w.ID) || !strings.Contains(req.Messages[0].Content, "Trusted run_id=") {
			t.Fatal("missing trusted workflow or unified event")
		}
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "worker completed"}, FinishReason: "stop"}
	}}
	s, err := agentservice.New(agentservice.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{"filter-test": pm, "worker-test": cm}})
	must(t, err)
	result, err := s.Run(ctx, u.ID, f.RunRequest{AgentID: parent.ID, Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	must(t, err)
	pr, err := repo.GetRun(ctx, u.ID, result.RunID)
	must(t, err)
	cr, err := repo.GetRun(ctx, u.ID, childRunID)
	must(t, err)
	if pr.WorkflowID != "" || pr.WorkflowSnapshot != nil || cr.ParentRunID != pr.ID || cr.WorkflowID != w.ID || cr.WorkflowVersion != w.Version || cr.WorkflowSnapshot == nil || string(cr.WorkflowSnapshot.Definition) != string(w.Definition) {
		t.Fatal("workflow scope/version audit not persisted", pr, cr)
	}
}
