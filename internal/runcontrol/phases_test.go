package runcontrol

import (
	"context"
	"testing"

	"academic/backend/internal/model"
)

func TestTriggerPhaseYieldsPendingItemsAndResumesWithoutAnotherTrigger(t *testing.T) {
	svc, repo, store, _, batch := newItemsIntegration(t, "")
	svc.process(context.Background(), batch)
	first := store.batches[batch.ID]
	if first.Status != "queued" || first.Stage != "items" || len(first.Trigger) == 0 {
		t.Fatalf("screening did not persist and yield: %+v", first)
	}
	root := stableItemID(batch.ID, "notice-change")
	if store.items[root].Status != "PENDING" {
		t.Fatalf("yield marked unprocessed item failed: %+v", store.items[root])
	}
	if store.items[stableItemID(batch.ID, "chat-change")].Status != "IGNORED" {
		t.Fatal("screening ignored decision not persisted")
	}
	for _, run := range store.runs {
		if run.AgentID != "trigger" {
			t.Fatalf("screening launched long processing before handoff: %+v", run)
		}
	}
	first.Status = "running"
	first.Attempt++
	svc.process(context.Background(), first)
	if store.batches[batch.ID].Status != "succeeded" {
		t.Fatal(store.batches[batch.ID])
	}
	triggers := 0
	for _, run := range repo.runs {
		if run.AgentID == "trigger" {
			triggers++
		}
	}
	if triggers != 1 {
		t.Fatalf("Trigger ran again after persistent handoff: %d", triggers)
	}
}

func TestItemsPhaseRejectsMissingTriggerWithoutStartingModel(t *testing.T) {
	svc, repo, store, _, batch := newItemsIntegration(t, "")
	batch.Stage = "items"
	svc.process(context.Background(), batch)
	got := store.batches[batch.ID]
	if got.Status != "failed" || got.Stage != "items" || got.Error != "INVALID_TRIGGER_OUTPUT" {
		t.Fatal(got)
	}
	if len(repo.runs) != 0 {
		t.Fatal("items recovery ran a fresh Trigger")
	}
	for _, item := range store.items {
		if item.Status != "FAILED" {
			t.Fatal(item)
		}
	}
}

func TestTriggerPhaseOnlyIgnoredEndsWithoutQueueHandoff(t *testing.T) {
	svc, _, store, _, batch := newItemsIntegration(t, "")
	batch.Trigger = []byte(`{"decision":"IGNORE","reason":"仅普通致谢","relevant_change_ids":[]}`)
	svc.process(context.Background(), batch)
	got := store.batches[batch.ID]
	if got.Status != "ignored" || got.Stage != "trigger" {
		t.Fatal(got)
	}
	for _, item := range store.items {
		if item.Status != "IGNORED" {
			t.Fatal(item)
		}
	}
}

var _ model.ControlRepository = (*itemsIntegrationStore)(nil)
