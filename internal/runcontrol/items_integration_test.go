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
	repo.agents["learner"] = itemsAgent("learner", []string{"workflow_create"}, []string{"workflow.write"})
	repo.agents["worker"] = itemsAgent("worker", []string{"data_query"}, []string{"data.read", "workflow.read"})
	store := &itemsIntegrationStore{batches: map[string]model.ControlBatch{}, items: map[string]model.ControlItem{}, runs: map[string]model.ControlRun{}, artifacts: map[string]model.ControlArtifact{}}
	data := &itemsIntegrationData{}
	providers := map[string]f.ModelProvider{}
	providers["trigger"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		return itemsFinal(map[string]any{"decision": "TRIGGER", "reason": "课程事项应处理，闲聊不处理", "relevant_change_ids": []string{"notice-change"}}), nil
	})
	providers["learner"] = itemsIntegrationModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		input := itemsUserInput(t, r)
		var task string
		_ = json.Unmarshal(input["task"], &task)
		results := itemsToolResults(t, r)
		if len(results) == 0 {
			return itemsCall("workflow_create", map[string]any{"name": task, "status": "active", "definition": map[string]any{"entity": map[string]string{"kind": "course", "id": "course/" + task, "name": task}, "goal": "只读核查具体课程"}}), nil
		}
		var w f.Workflow
		if err := json.Unmarshal(results[0], &w); err != nil {
			t.Fatal(err)
		}
		status, verified := "APPLIED", true
		if mode == "learner_unverified" && task == "数学分析A班" {
			verified = false
		}
		if mode == "learner_needs_info" && task == "数学分析A班" {
			status = "NEEDS_INFO"
		}
		if mode == "learner_no_change" {
			status, verified = "NO_CHANGE", false
		}
		return itemsFinal(map[string]any{"status": status, "verified": verified, "workflow_id": w.ID, "workflow_version": w.Version}), nil
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
		return itemsFinal(map[string]any{"status": "COMPLETED", "summary": "已精确核查课程事项", "artifacts": []map[string]any{{"type": "SUMMARY", "title": "课程处理结果", "description": "已读取原通知"}}}), nil
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
			items = append(items, map[string]any{"item_id": id, "parent_item_id": root, "title": courses[course], "status": "COMPLETED", "reason": "真实Worker已核查", "evidence_refs": refs, "routes": routes})
		}
		items = append(items, map[string]any{"item_id": root + ":5", "parent_item_id": root, "title": "无关致谢", "status": "IGNORED", "reason": "普通致谢没有行动要求", "evidence_refs": []string{"notice-change"}, "routes": []any{}})
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
			svc, _, store, _, batch := newItemsIntegration(t, mode)
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
