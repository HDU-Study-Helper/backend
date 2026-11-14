package dbbridge

import (
	f "academic/agent/framework"
	agentservice "academic/backend/internal/agent"
	"academic/backend/internal/model"
	"academic/backend/internal/runcontrol"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"regexp"
	"sync"
	"testing"
	"time"
)

func TestControlPersistentManualQueue(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "control", model.RoleAdmin)
	repo := db.Control()
	round, e := repo.StartRound(ctx, "instance")
	must(t, e)
	changes := []model.ControlChange{{Operation: "created", Resource: "message", RecordID: "m1", After: json.RawMessage(`{"text":"真实消息","token":"private","url":"https://example.org/view?token=private&id=1"}`), OccurredAt: time.Now().UTC()}}
	must(t, db.transaction(ctx, func(tx pgx.Tx) error {
		return captureControl(model.WithControlRound(ctx, round), tx, u.ID, "dws", changes)
	}))
	must(t, repo.Seal(ctx))
	v, e := repo.Overview(ctx)
	must(t, e)
	if len(v.Batches) != 0 {
		t.Fatal("sealed unfinished round")
	}
	must(t, repo.FinishRound(ctx, round, []model.RefreshResult{{Source: "dws", Created: 1}}, false))
	must(t, repo.Seal(ctx))
	must(t, repo.Seal(ctx))
	v, e = repo.Overview(ctx)
	must(t, e)
	if len(v.Batches) != 1 || v.Batches[0].Status != "pending" || len(v.Runs) != 0 {
		t.Fatalf("refresh must remain pending: %+v", v)
	}
	b, e := repo.GetBatch(ctx, v.Batches[0].ID)
	must(t, e)
	var after map[string]any
	must(t, json.Unmarshal(b.Changes[0].After, &after))
	if after["token"] != nil || after["url"] != "https://example.org/view?id=1" {
		t.Fatal("snapshot exposes credentials")
	}
	var wg sync.WaitGroup
	counts := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, e := repo.Activate(ctx, round, "", "", false)
			if e != nil {
				t.Error(e)
			}
			counts <- n
		}()
	}
	wg.Wait()
	close(counts)
	total := 0
	for n := range counts {
		total += n
	}
	if total != 1 {
		t.Fatal("duplicate activation", total)
	}
	claims := make(chan model.ControlBatch, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, e := repo.Claim(ctx, "instance")
			if e == nil {
				claims <- b
			} else if !errors.Is(e, model.ErrNotFound) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatal("duplicate claim", len(claims))
	}
	b = <-claims
	b.Status = "failed"
	b.Trigger = json.RawMessage(`{"decision":"TRIGGER"}`)
	must(t, repo.SetBatch(ctx, b))
	n, e := repo.Activate(ctx, "", "", b.ID, true)
	must(t, e)
	if n != 1 {
		t.Fatal("safe retry rejected")
	}
	b2, e := repo.Claim(ctx, "instance")
	must(t, e)
	if b2.Attempt != 2 {
		t.Fatal("attempt not incremented")
	}
	b.Status = "succeeded"
	if !errors.Is(repo.SetBatch(ctx, b), model.ErrConflict) {
		t.Fatal("stale attempt overwrote new lease")
	}
	run := model.ControlRun{ID: "test-run", BatchID: b.ID, OwnerID: u.ID, AgentID: "worker", WorkflowID: "wf", State: "succeeded", Output: `{"status":"COMPLETED"}`}
	must(t, repo.SaveRun(ctx, run))
	b2.Status = "failed"
	must(t, repo.SetBatch(ctx, b2))
	n, e = repo.Activate(ctx, "", "", b2.ID, true)
	must(t, e)
	if n != 0 {
		t.Fatal("unsafe workflow retry allowed")
	}
	got, e := repo.GetRun(ctx, run.ID)
	must(t, e)
	if got.Output != run.Output {
		t.Fatal("lost run output")
	}
}
func TestControlRestartAndPartition(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "restart", model.RoleAdmin)
	repo := db.Control()
	round, e := repo.StartRound(ctx, "dead")
	must(t, e)
	must(t, db.transaction(ctx, func(tx pgx.Tx) error {
		for _, s := range []string{"chaoxing", "dws"} {
			if e := captureControl(model.WithControlRound(ctx, round), tx, u.ID, s, []model.ControlChange{{Operation: "created", RecordID: s, Resource: "message", After: json.RawMessage(`{"text":"hello"}`)}}); e != nil {
				return e
			}
		}
		return nil
	}))
	_, e = db.pool.Exec(ctx, `UPDATE control_rounds SET heartbeat=now()-interval '2 minutes' WHERE id=$1`, round)
	must(t, e)
	must(t, repo.Heartbeat(ctx, "new"))
	must(t, repo.Seal(ctx))
	v, e := repo.Overview(ctx)
	must(t, e)
	if len(v.Batches) != 2 || v.Rounds[0].Status != "interrupted" {
		t.Fatal("restart lost partial diffs")
	}
	n, e := repo.Activate(ctx, round, "dws", "", false)
	must(t, e)
	if n != 1 {
		t.Fatal("platform selection failed")
	}
	b, e := repo.Claim(ctx, "dead")
	must(t, e)
	_, e = db.pool.Exec(ctx, `UPDATE control_batches SET heartbeat=now()-interval '2 minutes' WHERE id=$1`, b.ID)
	must(t, e)
	must(t, repo.Heartbeat(ctx, "new"))
	b, e = repo.GetBatch(ctx, b.ID)
	must(t, e)
	if b.Status != "interrupted" {
		t.Fatal("restart silently reran work")
	}
	_, e = repo.Claim(ctx, "new")
	if !errors.Is(e, model.ErrNotFound) {
		t.Fatal("recovered without manual review", e)
	}
}
func TestWorkflowDatabaseLockAcrossRepositories(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a := db.Agents().(f.WorkflowLocker)
	b := db.Agents().(f.WorkflowLocker)
	scope := f.Scope{OwnerID: "owner", WorkflowID: "wf"}
	release, e := a.LockWorkflow(ctx, scope)
	must(t, e)
	c, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, e = b.LockWorkflow(c, scope)
	if e == nil {
		t.Fatal("same workflow overlapped")
	}
	other, e := b.LockWorkflow(ctx, f.Scope{OwnerID: "owner", WorkflowID: "other"})
	must(t, e)
	other()
	release()
	next, e := b.LockWorkflow(ctx, scope)
	must(t, e)
	next()
}

type controlModelFunc func(context.Context, f.ModelRequest) (f.ModelResponse, error)

func (fn controlModelFunc) Complete(c context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	return fn(c, r)
}
func TestControlEndToEndParallelPlatformsSerialWorkflow(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	u := fixtureUser(t, db, "pipeline", model.RoleAdmin)
	registry := f.Registry{Repository: db.Agents()}
	makeAgent := func(role string, tools, perms []string, n int) f.AgentInstance {
		d := definition(role, role, tools, perms)
		d.Type = role
		d.Limits = f.Limits{MaxConcurrent: n, ModelCalls: 20}
		a, e := registry.Create(ctx, u.ID, d)
		must(t, e)
		return a
	}
	makeAgent("trigger", nil, nil, 4)
	makeAgent("filter", []string{"agent_dispatch"}, []string{"agent.dispatch", "workflow.read"}, 4)
	worker := makeAgent("worker", nil, nil, 16)
	wfs := []f.Workflow{}
	for _, name := range []string{"Host", "Other"} {
		w, e := db.Agents().SaveWorkflow(ctx, f.Scope{OwnerID: u.ID, AgentID: worker.ID}, f.Workflow{Name: name, Status: "active", Definition: mustEntityDefinition(name)}, true)
		must(t, e)
		wfs = append(wfs, w)
	}
	var mu sync.Mutex
	triggerActive, triggerMax, filterActive, filterMax, workerTotal, workerMax, modelCalls := 0, 0, 0, 0, 0, 0, 0
	activeWF := map[string]int{}
	done := func(v any) f.ModelResponse {
		raw, _ := json.Marshal(v)
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: string(raw)}, FinishReason: "stop"}
	}
	trustedRunID := regexp.MustCompile(`Trusted run_id=([^;\s]+);`)
	provider := controlModelFunc(func(c context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		mu.Lock()
		modelCalls++
		mu.Unlock()
		var envelope struct {
			Input json.RawMessage `json:"input"`
		}
		for _, m := range r.Messages {
			if m.Role == "user" {
				_ = json.Unmarshal([]byte(m.Content), &envelope)
			}
		}
		switch r.Config.Provider {
		case "trigger":
			mu.Lock()
			triggerActive++
			if triggerActive > triggerMax {
				triggerMax = triggerActive
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			defer func() { mu.Lock(); triggerActive--; mu.Unlock() }()
			var input struct {
				Changes []model.ControlChange `json:"changes"`
			}
			if e := json.Unmarshal(envelope.Input, &input); e != nil {
				return f.ModelResponse{}, e
			}
			ids := []string{}
			for _, v := range input.Changes {
				ids = append(ids, v.ID)
			}
			return done(map[string]any{"decision": "TRIGGER", "reason": "通知变化", "relevant_change_ids": ids}), nil
		case "filter":
			var itemInput struct {
				ItemID string `json:"item_id"`
			}
			_ = json.Unmarshal(envelope.Input, &itemInput)
			if r.Messages[len(r.Messages)-1].Role == "tool" {
				routes := []map[string]any{}
				i := 0
				for _, m := range r.Messages {
					if m.Role != "tool" {
						continue
					}
					var result struct {
						OK   bool        `json:"ok"`
						Data f.RunResult `json:"data"`
					}
					if e := json.Unmarshal([]byte(m.Content), &result); e != nil {
						return f.ModelResponse{}, e
					}
					if !result.OK {
						t.Errorf("dispatch failed: %s", m.Content)
					}
					routes = append(routes, map[string]any{"item_id": itemInput.ItemID, "target": "WORKER", "workflow_id": wfs[i].ID, "execution": result.Data})
					i++
				}
				mu.Lock()
				filterActive--
				mu.Unlock()
				return done(map[string]any{"items": []map[string]any{{"item_id": itemInput.ItemID, "title": "具体作业通知", "status": "COMPLETED", "reason": "两条专业路由已完成", "routes": routes, "evidence_refs": []string{}, "artifacts": []any{}}}}), nil
			}
			mu.Lock()
			filterActive++
			if filterActive > filterMax {
				filterMax = filterActive
			}
			mu.Unlock()
			calls := []f.ToolCall{}
			for _, w := range wfs {
				raw, _ := json.Marshal(map[string]any{"target_agent_id": worker.ID, "task": "处理通知", "workflow": map[string]any{"workflow_id": w.ID, "version": w.Version}, "context": map[string]any{"item_id": itemInput.ItemID}})
				calls = append(calls, f.ToolCall{ID: w.ID, Type: "function", Function: f.FunctionCall{Name: "agent_dispatch", Arguments: string(raw)}})
			}
			return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: calls}, FinishReason: "tool_calls"}, nil
		case "worker":
			var input struct {
				Workflow f.WorkflowSelection `json:"workflow"`
			}
			if e := json.Unmarshal(envelope.Input, &input); e != nil {
				return f.ModelResponse{}, e
			}
			mu.Lock()
			activeWF[input.Workflow.ID]++
			workerTotal++
			if activeWF[input.Workflow.ID] > 1 {
				t.Error("same workflow overlapped")
			}
			if workerTotal > workerMax {
				workerMax = workerTotal
			}
			mu.Unlock()
			time.Sleep(60 * time.Millisecond)
			mu.Lock()
			activeWF[input.Workflow.ID]--
			workerTotal--
			mu.Unlock()
			runID := ""
			for _, message := range r.Messages {
				if message.Role == "system" {
					if match := trustedRunID.FindStringSubmatch(message.Content); len(match) == 2 {
						runID = match[1]
					}
				}
			}
			if runID == "" {
				return f.ModelResponse{}, f.Fail("TEST", "missing trusted runtime identity")
			}
			workflow, e := db.Agents().GetWorkflow(c, u.ID, input.Workflow.ID)
			if e != nil {
				return f.ModelResponse{}, e
			}
			return done(map[string]any{"run_id": runID, "workflow_id": workflow.ID, "workflow_version": workflow.Version, "status": "COMPLETED", "summary": "已完成通知处理测试流程", "artifacts": []any{}, "actions": []any{}, "evidence_refs": []any{}, "evolution_feedback": []any{}, "interaction": nil, "collaboration": nil}), nil
		}
		return f.ModelResponse{}, f.Fail("TEST", "unexpected provider")
	})
	agents, e := agentservice.New(agentservice.Dependencies{Repository: db.Agents(), Providers: map[string]f.ModelProvider{"trigger": provider, "filter": provider, "worker": provider}})
	must(t, e)
	service := runcontrol.New(db.Control(), agents)
	round, e := service.StartRefresh(ctx)
	must(t, e)
	must(t, db.transaction(ctx, func(tx pgx.Tx) error {
		for _, source := range []string{"chaoxing", "dws"} {
			if e := captureControl(model.WithControlRound(ctx, round), tx, u.ID, source, []model.ControlChange{{Operation: "created", Resource: "message", RecordID: source, After: json.RawMessage(`{"text":"作业通知"}`), OccurredAt: time.Now().UTC()}}); e != nil {
				return e
			}
		}
		return nil
	}))
	must(t, service.Store.FinishRound(ctx, round, nil, false))
	must(t, service.Store.Seal(ctx))
	runDone := make(chan error, 1)
	go func() { runDone <- service.Run(ctx) }()
	time.Sleep(650 * time.Millisecond)
	mu.Lock()
	before := modelCalls
	mu.Unlock()
	if before != 0 {
		t.Fatal("refresh called model without manual start")
	}
	n, e := service.Store.Activate(ctx, round, "", "", false)
	must(t, e)
	if n != 2 {
		t.Fatal("platform batching", n)
	}
	complete := false
	for i := 0; i < 100; i++ {
		v, e := service.Store.Overview(ctx)
		must(t, e)
		if v.Counts["failed"] > 0 || v.Counts["partial"] > 0 {
			for _, b := range v.Batches {
				t.Logf("batch %+v", b)
			}
			t.Fatal("pipeline failed")
		}
		if v.Counts["succeeded"] == 2 {
			complete = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	cancel()
	<-runDone
	if !complete {
		t.Fatal("pipeline timed out")
	}
	mu.Lock()
	defer mu.Unlock()
	if triggerMax != 2 || filterMax != 2 || workerMax != 2 {
		t.Fatalf("parallelism trigger=%d filter=%d workflow=%d", triggerMax, filterMax, workerMax)
	}
}
