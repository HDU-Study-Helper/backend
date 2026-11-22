package runcontrol

import (
	f "academic/agent/framework"
	agentservice "academic/backend/internal/agent"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/model"
	"context"
	"testing"
)

type passiveTestRepo struct {
	*itemsIntegrationRepo
	c.Store
	archives map[string]c.Archive
	q        *c.Question
}

func (r *passiveTestRepo) GetRun(ctx context.Context, owner, id string) (f.RunRecord, error) {
	run := r.runs[id]
	if run.OwnerID != owner {
		return run, f.Fail("RESOURCE_NOT_FOUND", "run")
	}
	return run, nil
}
func (r *passiveTestRepo) SaveContinuation(ctx context.Context, a c.Archive, q *c.Question) error {
	r.archives[a.Transcript.Run] = a
	if q != nil {
		r.q = q
	}
	return nil
}
func (r *passiveTestRepo) LoadContinuation(ctx context.Context, owner, id string) (c.Archive, error) {
	return r.archives[id], nil
}
func (r *passiveTestRepo) ClaimContinuation(ctx context.Context, s c.Source, id, answer string) (c.Question, *f.RunResult, error) {
	v := *r.q
	r.q = nil
	return v, nil, nil
}
func (r *passiveTestRepo) FinishContinuation(context.Context, c.Source, string, f.RunResult) error {
	return nil
}
func (r *passiveTestRepo) PendingContinuations(ctx context.Context, s c.Source) ([]c.Question, error) {
	if r.q != nil && r.q.Key == s.Key {
		return []c.Question{*r.q}, nil
	}
	return nil, nil
}
func TestPassiveItemSuspendsAndReconcilesOriginalBatch(t *testing.T) {
	_, base, store, _, batch := newItemsIntegration(t, "learner_no_change")
	repo := &passiveTestRepo{itemsIntegrationRepo: base, archives: map[string]c.Archive{}}
	var w f.Workflow
	for _, w = range base.workflows {
		break
	}
	change := model.ControlChange{ID: "change", RecordID: "1", After: mustJSON(map[string]string{"title": "math"})}
	item := stableItemID(batch.ID, change.ID)
	pCalls, wCalls := 0, 0
	providers := map[string]f.ModelProvider{"filter": itemsIntegrationModel(func(ctx context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		pCalls++
		if pCalls == 1 {
			return itemsCall("agent_dispatch", map[string]any{"target_agent_id": "worker", "task": "check math", "workflow": map[string]any{"workflow_id": w.ID, "version": w.Version}}), nil
		}
		if pCalls == 2 {
			return itemsFinal(map[string]string{"status": "waiting"}), nil
		}
		return itemsFinal(map[string]any{"items": []any{map[string]any{"item_id": item, "status": "COMPLETED", "reason": "answered"}}}), nil
	}), "worker": itemsIntegrationModel(func(ctx context.Context, r f.ModelRequest) (f.ModelResponse, error) {
		wCalls++
		if wCalls == 1 {
			return itemsFinal(map[string]any{"interaction": map[string]string{"type": "TEXT_INPUT", "question": "Which class?"}, "status": "NEEDS_USER_INPUT"}), nil
		}
		if c.UserMessageRef(ctx, batch.OwnerID) == "" {
			t.Fatal("missing authenticated answer ref")
		}
		return itemsFinal(map[string]any{"status": "COMPLETED", "summary": "confirmed"}), nil
	})}
	a, e := agentservice.New(agentservice.Dependencies{Repository: repo, Providers: providers})
	if e != nil {
		t.Fatal(e)
	}
	s := New(store, a)
	ctx := context.Background()
	_, partial, e := s.processItems(ctx, batch, []model.ControlChange{change}, store)
	if e != nil || !partial || repo.q == nil || pCalls != 2 {
		t.Fatalf("unexpected passive outcome: partial=%v p=%d err=%v", partial, pCalls, e)
	}
	q := *repo.q
	if q.Key != item || store.items[item].Status != "NEEDS_USER_INPUT" {
		t.Fatal("unstable source or waiting status", q.Key, store.items[item])
	}
	_, _, e = s.processItems(ctx, batch, []model.ControlChange{change}, store)
	if e != nil || pCalls != 2 {
		t.Fatal("passive retry repeated underlying task", e)
	}
	_, e = a.Answer(c.WithSource(ctx, batch.OwnerID, c.ChA, item), batch.OwnerID, q.ID, "class A")
	if e != nil {
		t.Fatal(e)
	}
	v := store.items[item]
	if v.Status != "COMPLETED" || len(v.PendingQuestions) != 0 || len(v.ContinuationRuns) != 2 || wCalls != 2 || pCalls != 3 {
		t.Fatal("item not reconciled", v, pCalls, wCalls)
	}
	for _, id := range v.ContinuationRuns {
		r := store.runs[id]
		if r.BatchID != batch.ID || r.ItemID != item {
			t.Fatal("resumed run lost provenance", r)
		}
	}
}
