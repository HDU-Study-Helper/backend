package runcontrol

import (
	"context"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
)

type toolMemoryRepo struct {
	f.Repository
	memory f.Memory
	calls  int
	scope  f.Scope
	reject bool
}

func (r *toolMemoryRepo) GetMemory(_ context.Context, s f.Scope, id string) (f.Memory, error) {
	r.calls++
	r.scope = s
	if r.reject || id != r.memory.ID || s.OwnerID != r.memory.OwnerID {
		return f.Memory{}, f.Fail("PERMISSION_DENIED", "not allowed")
	}
	return r.memory, nil
}
func TestMemoryWriteArtifactVisibleWithoutFinalOutputAndIdempotent(t *testing.T) {
	repo := &toolMemoryRepo{memory: f.Memory{ID: "m", OwnerID: "owner", Title: "真实记忆", Content: "已保存的事实"}}
	service := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: repo}}}
	store := &artifactStore{values: map[string]model.ControlArtifact{}}
	item := model.ControlItem{ID: "item", OwnerID: "owner", BatchID: "batch", RoundID: "round"}
	run := model.ControlRun{ID: "run", OwnerID: "owner", AgentID: "worker", WorkflowID: "wf", ToolExecutions: mustJSON([]f.ToolExecution{{Name: "memory_create", ResultRefs: []f.ToolResultRef{{Kind: "MEMORY", ID: "m"}}}})}
	service.capturePersistedToolArtifacts(context.Background(), store, item, run)
	service.capturePersistedToolArtifacts(context.Background(), store, item, run)
	if len(store.values) != 1 || repo.scope.WorkflowID != "wf" || repo.scope.AgentID != "worker" {
		t.Fatal(store.values, repo.scope)
	}
	for _, v := range store.values {
		if v.Type != "MEMORY" || v.EntityRef != "m" || v.Title != "真实记忆" || v.RunID != "run" {
			t.Fatal(v)
		}
	}
}
func TestMemoryWriteArtifactRejectsScopeAndFailedTools(t *testing.T) {
	repo := &toolMemoryRepo{memory: f.Memory{ID: "m", OwnerID: "owner"}, reject: true}
	service := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: repo}}}
	store := &artifactStore{values: map[string]model.ControlArtifact{}}
	item := model.ControlItem{ID: "item", OwnerID: "owner"}
	run := model.ControlRun{ID: "run", OwnerID: "other", AgentID: "worker", ToolExecutions: mustJSON([]f.ToolExecution{{Name: "memory_create", ResultRefs: []f.ToolResultRef{{Kind: "MEMORY", ID: "m"}}}})}
	service.capturePersistedToolArtifacts(context.Background(), store, item, run)
	if repo.calls != 0 {
		t.Fatal("cross-owner lookup attempted")
	}
	run.OwnerID = "owner"
	service.capturePersistedToolArtifacts(context.Background(), store, item, run)
	if len(store.values) != 0 {
		t.Fatal("unauthorized memory captured")
	}
	repo.reject = false
	run.ToolExecutions = mustJSON([]f.ToolExecution{{Name: "memory_update", Error: f.Fail("INTERNAL_ERROR", "failed"), ResultRefs: []f.ToolResultRef{{Kind: "MEMORY", ID: "m"}}}})
	service.capturePersistedToolArtifacts(context.Background(), store, item, run)
	if len(store.values) != 0 {
		t.Fatal("failed write captured")
	}
}
