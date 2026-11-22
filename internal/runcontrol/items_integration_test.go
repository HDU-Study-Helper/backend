package runcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	stage "academic/backend/internal/learnerstage"
	"academic/backend/internal/model"
)

// These tests exercise the actual Runtime, builtin tools, internal dispatch,
// observer and final batch/item gates. Only persistence and the model are fakes.
type itemsIntegrationRepo struct {
	f.Repository
	mu        sync.Mutex
	agents    map[string]f.AgentInstance
	workflows map[string]f.Workflow
	runs      map[string]f.RunRecord
	snapshots map[string]stage.Snapshot
	receipts  map[string]stage.Receipt
	commits   map[string]string
	creations int
}

func (r *itemsIntegrationRepo) GetAgent(_ context.Context, s f.Scope) (f.AgentInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[s.AgentID]
	if !ok || a.OwnerID != s.OwnerID {
		return a, f.Fail("AGENT_NOT_FOUND", "not owned")
	}
	return a, nil
}
func (r *itemsIntegrationRepo) ListAgents(_ context.Context, owner string, _ int, _ string) ([]f.AgentInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []f.AgentInstance{}
	for _, a := range r.agents {
		if a.OwnerID == owner {
			out = append(out, a)
		}
	}
	return out, nil
}
func (*itemsIntegrationRepo) ListMemory(context.Context, f.Scope, string, int) ([]f.Memory, error) {
	return []f.Memory{}, nil
}
func (*itemsIntegrationRepo) SearchMemory(context.Context, f.Scope, f.MemorySearch) ([]f.Memory, error) {
	return []f.Memory{}, nil
}
func (r *itemsIntegrationRepo) GetMemory(_ context.Context, s f.Scope, id string) (f.Memory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, snapshot := range r.snapshots {
		for _, m := range snapshot.Memories {
			if m.ID == id && m.OwnerID == s.OwnerID && (m.WorkflowID == s.WorkflowID && s.WorkflowID != "" || m.AgentID == s.AgentID || m.Visibility == "PUB") {
				return stage.Clone(m), nil
			}
		}
	}
	return f.Memory{}, f.Fail("RESOURCE_NOT_FOUND", "memory not owned")
}
func (r *itemsIntegrationRepo) LearnerSnapshot(_ context.Context, s f.Scope, id string) (stage.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workflows[id]
	if !ok || w.OwnerID != s.OwnerID || s.AgentID != "learner" {
		return stage.Snapshot{}, f.Fail("RESOURCE_NOT_FOUND", "workflow not owned")
	}
	if snap, exists := r.snapshots[id]; exists {
		return stage.Clone(snap), nil
	}
	return stage.Snapshot{Workflow: stage.Clone(w), Memories: []f.Memory{}, Relations: []f.MemoryRelation{}}, nil
}
func (r *itemsIntegrationRepo) CommitLearnerStage(_ context.Context, c stage.Commit) (stage.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Scope.OwnerID != "owner" || c.Scope.AgentID != "learner" || c.SessionID == "" || c.Revision < 1 {
		return stage.Receipt{}, f.Fail("PERMISSION_DENIED", "invalid staging scope")
	}
	if r.receipts == nil {
		r.receipts, r.commits = map[string]stage.Receipt{}, map[string]string{}
	}
	if r.snapshots == nil {
		r.snapshots = map[string]stage.Snapshot{}
	}
	payload := string(mustJSON(c))
	if receipt, ok := r.receipts[c.SessionID]; ok {
		if r.commits[c.SessionID] != payload {
			return stage.Receipt{}, f.Fail("CONFLICT", "staging payload changed")
		}
		return stage.Clone(receipt), nil
	}
	if c.Base != nil {
		current, ok := r.snapshots[c.Base.Workflow.ID]
		if !ok || stage.Hash(current) != c.BaselineHash || stage.Hash(*c.Base) != c.BaselineHash {
			return stage.Receipt{}, f.Fail("CONFLICT", "staging baseline changed")
		}
	}
	desired := stage.Clone(c.Desired)
	w := desired.Workflow
	receipt := stage.Receipt{SessionID: c.SessionID, Revision: c.Revision, Operation: c.Operation, Status: "COMMITTED", Verified: true, MemoryIDs: []string{}, IDMap: map[string]string{}}
	switch c.Operation {
	case "CREATE":
		if c.Base != nil || !model.ValidWorkflowEntity(w.Definition) || w.Status != "active" || len(desired.Memories) == 0 {
			return stage.Receipt{}, f.Fail("INVALID_ARGUMENT", "invalid course initialization")
		}
		r.creations++
		stagedID := w.ID
		w.ID, w.OwnerID, w.CreatedBy, w.Version = fmt.Sprintf("wf-%d", r.creations), c.Scope.OwnerID, c.Scope.AgentID, 1
		receipt.IDMap[stagedID] = w.ID
	case "NONE":
		if c.Base == nil || stage.Hash(desired) != c.BaselineHash {
			return stage.Receipt{}, f.Fail("INVALID_ARGUMENT", "NO_CHANGE has a diff")
		}
		receipt.Status = "NO_CHANGE"
	case "UPDATE":
		if c.Base == nil || w.ID != c.Base.Workflow.ID || !model.ValidWorkflowEntity(w.Definition) {
			return stage.Receipt{}, f.Fail("INVALID_ARGUMENT", "invalid staged update")
		}
		w.Version = c.Base.Workflow.Version
		if string(mustJSON(w)) != string(mustJSON(c.Base.Workflow)) {
			w.Version++
		}
	default:
		return stage.Receipt{}, f.Fail("INVALID_ARGUMENT", "unsupported integration staging operation")
	}
	for i := range desired.Memories {
		m := &desired.Memories[i]
		if m.OwnerID != c.Scope.OwnerID || m.WorkflowID != c.Desired.Workflow.ID || len(m.SourceRefs) == 0 {
			return stage.Receipt{}, f.Fail("PERMISSION_DENIED", "invalid staged memory ownership or source")
		}
		oldID := m.ID
		if c.Operation == "CREATE" {
			m.ID = f.NewID()
		}
		m.WorkflowID = w.ID
		receipt.IDMap[oldID], receipt.MemoryIDs = m.ID, append(receipt.MemoryIDs, m.ID)
	}
	for i := range desired.Relations {
		v := &desired.Relations[i]
		v.SourceID, v.TargetID = receipt.IDMap[v.SourceID], receipt.IDMap[v.TargetID]
	}
	desired.Workflow = w
	r.workflows[w.ID], r.snapshots[w.ID] = w, desired
	receipt.WorkflowID, receipt.WorkflowVersion = w.ID, w.Version
	r.receipts[c.SessionID], r.commits[c.SessionID] = stage.Clone(receipt), payload
	return receipt, nil
}
func (r *itemsIntegrationRepo) LearnerStageReceipt(_ context.Context, owner, session string) (stage.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if receipt, ok := r.receipts[session]; ok && owner == "owner" {
		return stage.Clone(receipt), nil
	}
	return stage.Receipt{}, f.Fail("RESOURCE_NOT_FOUND", "staging receipt not found")
}
func (r *itemsIntegrationRepo) SaveRun(_ context.Context, v f.RunRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[v.ID] = v
	return nil
}
func (r *itemsIntegrationRepo) GetWorkflow(_ context.Context, owner, id string) (f.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.workflows[id]
	if !ok || v.OwnerID != owner {
		return v, f.Fail("RESOURCE_NOT_FOUND", "workflow not owned")
	}
	return v, nil
}
func (r *itemsIntegrationRepo) ListWorkflows(_ context.Context, owner string, _ int, _ string) ([]f.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []f.Workflow{}
	for _, w := range r.workflows {
		if w.OwnerID == owner {
			out = append(out, w)
		}
	}
	return out, nil
}
func (r *itemsIntegrationRepo) SaveWorkflow(_ context.Context, s f.Scope, w f.Workflow, create bool) (f.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !create || s.OwnerID != "owner" || s.AgentID != "learner" || !model.ValidWorkflowEntity(w.Definition) {
		return w, f.Fail("INVALID_ARGUMENT", "invalid scoped workflow")
	}
	r.creations++
	w.ID = fmt.Sprintf("wf-%d", r.creations)
	w.OwnerID = s.OwnerID
	w.CreatedBy = s.AgentID
	w.Version = 1
	w.CreatedAt = time.Now()
	w.UpdatedAt = w.CreatedAt
	r.workflows[w.ID] = w
	return w, nil
}

type itemsIntegrationStore struct {
	model.ControlRepository
	model.ControlItemRepository
	mu        sync.Mutex
	batches   map[string]model.ControlBatch
	items     map[string]model.ControlItem
	runs      map[string]model.ControlRun
	artifacts map[string]model.ControlArtifact
}

func (r *itemsIntegrationStore) SetBatch(_ context.Context, v model.ControlBatch) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches[v.ID] = v
	return nil
}
func (r *itemsIntegrationStore) SaveRun(_ context.Context, v model.ControlRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[v.ID] = v
	return nil
}
func (r *itemsIntegrationStore) BatchRuns(_ context.Context, batch string) ([]model.ControlRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []model.ControlRun{}
	for _, v := range r.runs {
		if v.BatchID == batch {
			out = append(out, v)
		}
	}
	return out, nil
}
func (r *itemsIntegrationStore) SaveItem(_ context.Context, v model.ControlItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[v.ID] = v
	return nil
}
func (r *itemsIntegrationStore) GetItem(_ context.Context, id string) (model.ControlItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.items[id]
	if !ok {
		return v, model.ErrNotFound
	}
	return v, nil
}
func (r *itemsIntegrationStore) SaveArtifact(_ context.Context, v model.ControlArtifact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.artifacts[v.ID] = v
	return nil
}

type itemsIntegrationData struct {
	mu      sync.Mutex
	queries int
}

func (*itemsIntegrationData) Catalog(context.Context, f.Scope) ([]f.Dataset, error) {
	fields := []f.Field{}
	for _, name := range []string{"id", "connection_id", "source", "kind", "external_id", "parent_id", "title", "text"} {
		fields = append(fields, f.Field{Name: name, Type: "string"})
	}
	return []f.Dataset{{Name: "platform_records", Fields: fields}}, nil
}
func (d *itemsIntegrationData) Query(_ context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	if s.OwnerID != "owner" {
		return f.QueryResult{}, f.Fail("PERMISSION_DENIED", "owner")
	}
	d.mu.Lock()
	d.queries++
	d.mu.Unlock()
	row := map[string]any{"id": "10", "connection_id": "connection", "source": "chaoxing", "kind": "notice", "external_id": "notice-native", "parent_id": "", "title": "三门课通知", "text": "数学作业；英语考试；物理报告；数学补充；谢谢"}
	for _, filter := range q.Filters {
		if filter.Operator != "eq" || row[filter.Field] != filter.Value {
			return f.QueryResult{Rows: []map[string]any{}}, nil
		}
	}
	out := map[string]any{}
	for _, field := range q.Fields {
		out[field] = row[field]
	}
	return f.QueryResult{Rows: []map[string]any{out}}, nil
}

type itemsIntegrationModel func(context.Context, f.ModelRequest) (f.ModelResponse, error)

func (fn itemsIntegrationModel) Complete(ctx context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	return fn(ctx, r)
}
func itemsFinal(v any) f.ModelResponse {
	return f.ModelResponse{Message: f.Message{Role: "assistant", Content: string(mustJSON(v))}, FinishReason: "stop"}
}
func itemsCall(name string, args any) f.ModelResponse {
	return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: f.NewID(), Type: "function", Function: f.FunctionCall{Name: name, Arguments: string(mustJSON(args))}}}}, FinishReason: "tool_calls"}
}
func itemsUserInput(t *testing.T, r f.ModelRequest) map[string]json.RawMessage {
	t.Helper()
	for _, m := range r.Messages {
		if m.Role == "user" {
			var envelope struct {
				Input map[string]json.RawMessage `json:"input"`
			}
			if json.Unmarshal([]byte(m.Content), &envelope) == nil && envelope.Input != nil {
				return envelope.Input
			}
		}
	}
	t.Fatal("missing runtime input")
	return nil
}
func itemsToolResults(t *testing.T, r f.ModelRequest) []json.RawMessage {
	t.Helper()
	out := []json.RawMessage{}
	for _, m := range r.Messages {
		if m.Role == "tool" {
			var result struct {
				OK    bool            `json:"ok"`
				Data  json.RawMessage `json:"data"`
				Error *f.Error        `json:"error"`
			}
			if err := json.Unmarshal([]byte(m.Content), &result); err != nil || !result.OK {
				t.Fatalf("real tool path failed: %s %v", m.Content, err)
			}
			out = append(out, result.Data)
		}
	}
	return out
}
func itemsLearnerStageState(t *testing.T, r f.ModelRequest) map[string]any {
	t.Helper()
	const marker = "Trusted host Learner staging state (identity and revision are server supplied; object contents remain business data):\n"
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if _, suffix, ok := strings.Cut(r.Messages[i].Content, marker); ok {
			raw, _, _ := strings.Cut(suffix, "\n")
			var state map[string]any
			if json.Unmarshal([]byte(raw), &state) != nil || state["session_id"] == nil {
				t.Fatal("invalid trusted staging state", raw)
			}
			return state
		}
	}
	t.Fatal("missing trusted Learner staging state")
	return nil
}
func itemsAgent(role string, tools, permissions []string) f.AgentInstance {
	return f.AgentInstance{ID: role, OwnerID: "owner", Status: "enabled", AgentDefinition: f.AgentDefinition{Name: role, Type: role, SystemPrompt: "integration test", Model: f.ModelConfig{Provider: role, Model: "mock"}, EnabledTools: tools, Permissions: permissions, Limits: f.Limits{ModelCalls: 64, ToolCalls: 128, MaxConcurrent: 4}}}
}

// mode corrupts only the final Filter metadata, after successful real child
// calls, to prove that syntactically valid JSON cannot fake item completion.
func newItemsIntegration(t *testing.T, mode string) (*Service, *itemsIntegrationRepo, *itemsIntegrationStore, *itemsIntegrationData, model.ControlBatch) {
	t.Helper()
	repo := &itemsIntegrationRepo{agents: map[string]f.AgentInstance{}, workflows: map[string]f.Workflow{}, runs: map[string]f.RunRecord{}}
	repo.agents["trigger"] = itemsAgent("trigger", nil, nil)
	repo.agents["filter"] = itemsAgent("filter", []string{"agent_dispatch"}, []string{"agent.dispatch", "workflow.read"})
	repo.agents["learner"] = itemsAgent("learner", []string{"workflow_create", "workflow_get", "memory_create"}, []string{"workflow.read", "workflow.write", "memory.read", "memory.write"})
	repo.agents["worker"] = itemsAgent("worker", []string{"data_query"}, []string{"data.read", "workflow.read"})
	if mode == "learner_no_change" {
		repo.snapshots = map[string]stage.Snapshot{}
		for _, course := range []string{"数学分析A班", "大学英语B班", "大学物理C班"} {
			w := f.Workflow{ID: "existing/" + course, OwnerID: "owner", CreatedBy: "learner", Name: course, Status: "active", Version: 2, Definition: mustJSON(map[string]any{"entity": map[string]string{"kind": "course", "id": "course/" + course, "name": course}, "goal": "只读核查具体课程"})}
			repo.workflows[w.ID] = w
			repo.snapshots[w.ID] = stage.Snapshot{Workflow: w, Memories: []f.Memory{{ID: "memory/" + course, OwnerID: "owner", WorkflowID: w.ID, Visibility: "PRI", MemoryType: "BASE", Level: "L1", Title: course + "来源概况", Content: "已核实的课程背景", Status: "active", SourceRefs: mustJSON([]string{"notice-change"})}}, Relations: []f.MemoryRelation{}}
		}
	}
	store := &itemsIntegrationStore{batches: map[string]model.ControlBatch{}, items: map[string]model.ControlItem{}, runs: map[string]model.ControlRun{}, artifacts: map[string]model.ControlArtifact{}}
	data := &itemsIntegrationData{}
	providers := map[string]f.ModelProvider{}
	providers["trigger"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		return itemsFinal(map[string]any{"decision": "TRIGGER", "reason": "课程事项应处理，闲聊不处理", "relevant_indices": []int{1}}), nil
	})
	providers["learner"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		input := itemsUserInput(t, r)
		var task string
		_ = json.Unmarshal(input["task"], &task)
		state := itemsLearnerStageState(t, r)
		results := itemsToolResults(t, r)
		entity := map[string]string{"kind": "course", "id": "course/" + task, "name": task}
		if len(results) == 0 {
			if mode == "learner_no_change" {
				return itemsCall("learner_stage_select", map[string]any{"workflow_id": "existing/" + task}), nil
			}
			return itemsCall("workflow_create", map[string]any{"name": task, "status": "active", "definition": map[string]any{"entity": entity, "goal": "只读核查具体课程"}}), nil
		}
		if len(results) == 1 && mode != "learner_no_change" {
			return itemsCall("memory_create", map[string]any{"title": task + "来源概况", "content": "来自原通知的具体课程要求与可追溯课程身份。", "source_refs": []string{"notice-change"}}), nil
		}
		// Exercise the explicit post-edit state tool, not a fabricated session/revision.
		if len(results) == 1 || len(results) == 2 && mode != "learner_no_change" {
			return itemsCall("learner_stage_state", map[string]any{}), nil
		}
		status, operation := "READY", "CREATE"
		var completion any = map[string]any{"marker": "READY_TO_COMMIT", "session_id": state["session_id"], "staging_revision": state["staging_revision"]}
		if mode == "learner_no_change" {
			status, operation = "NO_CHANGE", "NONE"
		}
		if mode == "learner_needs_info" && task == "数学分析A班" {
			status, completion = "NEEDS_INFO", nil
		}
		if mode == "learner_unverified" && task == "数学分析A班" {
			completion = nil
		}
		return itemsFinal(map[string]any{"protocol": stage.Protocol, "session_id": state["session_id"], "path": state["path"], "request_id": nil, "item_id": nil, "parent_item_id": nil, "workflow_id": state["workflow_id"], "base_version": state["base_version"], "entity": entity, "status": status, "operation": operation, "completion": completion, "summary": "Workflow管理结果", "reason": "根据真实暂存结果请求提交或保留阻断", "evidence_refs": []string{"notice-change"}, "changes": []any{}, "questions": []any{}, "limitations": []any{}}), nil
	})
	providers["worker"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		results := itemsToolResults(t, r)
		if len(results) == 0 {
			return itemsCall("data_query", map[string]any{"dataset": "platform_records", "fields": []string{"id", "title", "text"}, "filters": []map[string]string{{"field": "id", "operator": "eq", "value": "10"}}, "limit": 1}), nil
		}
		var result f.QueryResult
		_ = json.Unmarshal(results[0], &result)
		if len(result.Rows) != 1 || result.Rows[0]["text"] == "" {
			t.Fatal("Worker did not read actual mock source")
		}
		input := itemsUserInput(t, r)
		var workflow f.WorkflowSelection
		if err := json.Unmarshal(input["workflow"], &workflow); err != nil {
			t.Fatal(err)
		}
		var runID string
		for _, message := range r.Messages {
			if message.Role == "system" {
				_, suffix, found := strings.Cut(message.Content, "Trusted run_id=")
				if found {
					runID, _, _ = strings.Cut(suffix, ";")
				}
			}
		}
		if runID == "" {
			t.Fatal("missing trusted runtime identity")
		}
		return itemsFinal(map[string]any{"run_id": runID, "workflow_id": workflow.ID, "workflow_version": workflow.Version, "status": "COMPLETED", "summary": "已精确核查课程事项", "artifacts": []map[string]any{{"type": "SUMMARY", "title": "课程处理结果", "description": "已读取原通知", "entity_ref": nil, "data": map[string]any{"draft": true}}}, "actions": []any{}, "evidence_refs": []any{}, "interaction": nil, "collaboration": nil, "evolution_feedback": []any{}}), nil
	})
	providers["filter"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		input := itemsUserInput(t, r)
		var root string
		_ = json.Unmarshal(input["item_id"], &root)
		if root == "" {
			t.Fatal("processItems did not provide stable root")
		}
		var event map[string]any
		_ = json.Unmarshal(mustJSON(input), &event)
		if (mode == "learner_unverified" || mode == "learner_needs_info") && len(r.Messages) > 0 && r.Messages[len(r.Messages)-1].Role == "tool" {
			var envelope struct {
				OK    bool        `json:"ok"`
				Data  f.RunResult `json:"data"`
				Error *f.Error    `json:"error"`
			}
			if err := json.Unmarshal([]byte(r.Messages[len(r.Messages)-1].Content), &envelope); err != nil {
				t.Fatal(err)
			}
			var learner struct {
				Status     string `json:"status"`
				WorkflowID string `json:"workflow_id"`
			}
			_ = json.Unmarshal([]byte(envelope.Data.Output), &learner)
			if !envelope.OK || learner.Status == "NEEDS_INFO" {
				var childID string
				var actualError any
				if envelope.Error != nil {
					actualError = envelope.Error
				}
				store.mu.Lock()
				for _, run := range store.runs {
					if run.AgentID == "learner" {
						childID = run.ID
					}
				}
				store.mu.Unlock()
				if childID == "" {
					t.Fatal("blocked Learner run was not audited")
				}
				return itemsFinal(map[string]any{"items": []map[string]any{{"item_id": root + ":1", "parent_item_id": root, "title": "数学分析A班", "status": "PARTIAL", "reason": "Learner未返回已核验的创建或复用结果", "evidence_refs": []string{"notice-change"}, "routes": []map[string]any{{"item_id": root + ":1", "target": "LEARNER", "workflow_id": nil, "execution": map[string]any{"run_id": childID, "error": actualError}}}, "artifacts": []any{}}}}), nil
			}
		}
		results := itemsToolResults(t, r)
		dispatch := func(target, task string, wf *f.Workflow) f.ModelResponse {
			args := map[string]any{"target_agent_id": target, "task": task, "context": map[string]any{"event": event}}
			if wf != nil {
				args["workflow"] = map[string]any{"workflow_id": wf.ID, "version": wf.Version}
			}
			return itemsCall("agent_dispatch", args)
		}
		courses := []string{"数学分析A班", "大学英语B班", "大学物理C班"}
		workflows := []f.Workflow{}
		for i := 0; i < 3 && 2*i < len(results); i++ {
			var run f.RunResult
			_ = json.Unmarshal(results[2*i], &run)
			var out struct {
				ID string `json:"workflow_id"`
			}
			_ = json.Unmarshal([]byte(run.Output), &out)
			w, err := repo.GetWorkflow(context.Background(), "owner", out.ID)
			if err != nil {
				t.Fatal(err)
			}
			workflows = append(workflows, w)
		}
		if len(results) < 6 {
			course := len(results) / 2
			if len(results)%2 == 0 {
				return dispatch("learner", courses[course], nil), nil
			}
			return dispatch("worker", courses[course], &workflows[course]), nil
		}
		if len(results) == 6 {
			return dispatch("worker", "数学分析A班补充要求", &workflows[0]), nil
		}
		items := []map[string]any{}
		for i, idx := range []int{1, 3, 5, 6} {
			var worker f.RunResult
			_ = json.Unmarshal(results[idx], &worker)
			course := i
			if course == 3 {
				course = 0
			}
			var learner f.RunResult
			if i < 3 {
				_ = json.Unmarshal(results[2*i], &learner)
			}
			routes := []map[string]any{}
			id := fmt.Sprintf("%s:%d", root, i+1)
			if i < 3 {
				routes = append(routes, map[string]any{"item_id": id, "target": "LEARNER", "workflow_id": workflows[course].ID, "execution": map[string]any{"run_id": learner.RunID}})
			}
			runID := worker.RunID
			if mode == "fabricated_run" && i == 0 {
				runID = "never-executed"
			}
			if mode == "reused_run" && i == 3 {
				var first f.RunResult
				_ = json.Unmarshal(results[1], &first)
				runID = first.RunID
			}
			routes = append(routes, map[string]any{"item_id": id, "target": "WORKER", "workflow_id": workflows[course].ID, "execution": map[string]any{"run_id": runID}})
			refs := []string{"notice-change"}
			if mode == "unknown_ref" && i == 0 {
				refs = []string{"invented-record"}
			}
			items = append(items, map[string]any{"item_id": id, "parent_item_id": root, "title": courses[course], "status": "COMPLETED", "reason": "真实Worker已核查", "evidence_refs": refs, "routes": routes, "artifacts": []any{}})
		}
		items = append(items, map[string]any{"item_id": root + ":5", "parent_item_id": root, "title": "无关致谢", "status": "IGNORED", "reason": "普通致谢没有行动要求", "evidence_refs": []string{"notice-change"}, "routes": []any{}, "artifacts": []any{}})
		return itemsFinal(map[string]any{"items": items}), nil
	})
	agents, err := agent.New(agent.Dependencies{Repository: repo, Providers: providers, Backend: agent.BackendServices{Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store, agents, data)
	batch := model.ControlBatch{ID: "batch", RoundID: "round", OwnerID: "owner", Source: "chaoxing", Status: "queued", Attempt: 1, Changes: []model.ControlChange{{ID: "notice-change", RecordID: "10", ConnectionID: "connection", Source: "chaoxing", Resource: "notice", Operation: "created", After: json.RawMessage(`{"title":"三门课通知","external_id":"notice-native","content":"数学作业；英语考试；物理报告；数学补充；谢谢"}`)}, {ID: "chat-change", Source: "chaoxing", Resource: "discussion", Operation: "created", After: json.RawMessage(`{"title":"谢谢老师"}`)}}}
	return svc, repo, store, data, batch
}

func processIntegrationBatchToTerminal(t *testing.T, svc *Service, store *itemsIntegrationStore, batch model.ControlBatch) {
	t.Helper()
	for phase := 0; phase < 3; phase++ {
		svc.process(context.Background(), batch)
		store.mu.Lock()
		batch = store.batches[batch.ID]
		store.mu.Unlock()
		if batch.Status != "queued" {
			return
		}
		// Mirror a new persistent Claim, including a fresh attempt token.
		batch.Status = "running"
		batch.Attempt++
	}
	t.Fatal("batch failed to reach terminal state after phase handoff")
}

func TestItemsIntegrationMultiLearnerCoursesAndSameWorkflowPersist(t *testing.T) {
	svc, repo, store, data, batch := newItemsIntegration(t, "")
	processIntegrationBatchToTerminal(t, svc, store, batch)
	final := store.batches[batch.ID]
	if final.Status != "succeeded" {
		t.Fatalf("batch did not complete: %+v", final)
	}
	if repo.creations != 3 || len(repo.workflows) != 3 {
		t.Fatalf("expected three actual workflow_create calls, got %d", repo.creations)
	}
	root := stableItemID(batch.ID, "notice-change")
	if len(store.items) != 7 {
		t.Fatalf("want root + 5 subitems + Trigger ignored change, got %d", len(store.items))
	}
	for i := 1; i <= 4; i++ {
		item := store.items[fmt.Sprintf("%s:%d", root, i)]
		if item.Status != "COMPLETED" || item.ParentID != root || item.FilterRunID == "" {
			t.Fatalf("subitem not persisted: %+v", item)
		}
	}
	if store.items[root+":5"].Status != "IGNORED" || store.items[stableItemID(batch.ID, "chat-change")].Status != "IGNORED" {
		t.Fatal("mixed irrelevant information lost")
	}
	var summary struct {
		Items []model.ControlItem `json:"items"`
	}
	if json.Unmarshal(final.Filter, &summary) != nil || len(summary.Items) != 5 {
		t.Fatalf("final batch lost item coverage: %s", final.Filter)
	}
	workers := []model.ControlRun{}
	learners := 0
	for _, run := range store.runs {
		if run.AgentID == "worker" {
			workers = append(workers, run)
		}
		if run.AgentID == "learner" {
			learners++
		}
		if run.State != "succeeded" {
			t.Fatalf("actual runtime failed: %+v", run)
		}
	}
	if len(workers) != 4 || learners != 3 {
		t.Fatalf("missing actual dispatch: workers=%d learners=%d", len(workers), learners)
	}
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, run := range workers {
		counts[run.WorkflowID]++
		ids[run.ID] = true
		if run.BusinessStatus != "COMPLETED" || run.ParentID == "" {
			t.Fatal("worker final state not observed")
		}
	}
	values := []int{}
	for _, count := range counts {
		values = append(values, count)
	}
	sort.Ints(values)
	if fmt.Sprint(values) != "[1 1 2]" || len(ids) != 4 {
		t.Fatalf("same workflow must allow distinct runs: %v", counts)
	}
	if len(store.artifacts) < 7 {
		t.Fatalf("expected 3 workflow plus 4 real worker artifacts, got %d", len(store.artifacts))
	}
	if data.queries < 5 {
		t.Fatal("runtime/data enrichment not exercised")
	}
}

func TestItemsIntegrationLearnerOutcomeMustMatchActiveEntityRegistry(t *testing.T) {
	for _, mode := range []string{"learner_unverified", "learner_needs_info", "learner_no_change"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, store, _, batch := newItemsIntegration(t, mode)
			processIntegrationBatchToTerminal(t, svc, store, batch)
			root := stableItemID(batch.ID, "notice-change")
			if mode == "learner_no_change" {
				if store.batches[batch.ID].Status != "succeeded" || store.items[root].Status != "COMPLETED" {
					t.Fatal("verified Registry reuse incorrectly rejected")
				}
				return
			}
			if store.batches[batch.ID].Status != "partial" || store.items[root].Status != "PARTIAL" || store.items[root+":1"].Status != "PARTIAL" {
				t.Fatal("Learner declaration bypassed item or parent gate")
			}
			if !strings.Contains(store.items[root+":1"].Reason, "Learner未返回已核验") {
				t.Fatal(store.items[root+":1"].Reason)
			}
			if repo.creations != 0 || len(repo.workflows) != 0 || len(repo.receipts) != 0 {
				t.Fatal("unverified/blocked Learner output committed formal state", repo.creations, len(repo.workflows), len(repo.receipts))
			}
		})
	}
}

func TestItemsIntegrationRejectsInventedEvidenceAndChildExecution(t *testing.T) {
	for _, mode := range []string{"unknown_ref", "fabricated_run", "reused_run"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, store, _, batch := newItemsIntegration(t, mode)
			processIntegrationBatchToTerminal(t, svc, store, batch)
			final := store.batches[batch.ID]
			if final.Status != "partial" {
				t.Fatalf("valid JSON bypassed real execution/evidence gates: %+v", final)
			}
			root := stableItemID(batch.ID, "notice-change")
			which := root + ":1"
			if mode == "reused_run" {
				which = root + ":4"
			}
			if store.items[root].Status != "PARTIAL" {
				t.Fatalf("split parent hid incomplete children: %+v", store.items[root])
			}
			item := store.items[which]
			if item.Status != "PARTIAL" {
				t.Fatalf("unverified item accepted: %+v", item)
			}
			want := "证据引用"
			if mode != "unknown_ref" {
				want = "子运行"
			}
			if !strings.Contains(item.Reason, want) {
				t.Fatalf("missing specific validation cause: %+v", item)
			}
			for _, v := range store.items {
				if v.Status == "PENDING" || v.Status == "RUNNING" {
					t.Fatalf("nonterminal item left behind: %+v", v)
				}
			}
		})
	}
}
