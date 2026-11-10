package runcontrol

import (
	"context"
	"testing"

	"academic/backend/internal/model"
)

// Cancellation before item execution marks pending items FAILED. A later manual
// retry must process them, while never replaying an item with execution evidence.
func TestCancelledBatchRetryProcessesOnlyUntouchedItems(t *testing.T) {
	for _, executed := range []string{"none", "filter_id", "observer_run", "other_failure"} {
		t.Run(executed, func(t *testing.T) {
			svc, repo, store, _, batch := newItemsIntegration(t, "")
			batch.Stage = "items"
			batch.Trigger = mustJSON(map[string]any{"decision": "TRIGGER", "reason": "process", "relevant_change_ids": []string{"notice-change"}})
			for _, c := range batch.Changes {
				item := newControlItem(batch, c)
				store.items[item.ID] = item
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			svc.process(cancelled, batch)
			root := stableItemID(batch.ID, "notice-change")
			item := store.items[root]
			if store.batches[batch.ID].Status != "interrupted" || item.Status != "FAILED" || item.FilterRunID != "" {
				t.Fatalf("cancellation state: %+v %+v", store.batches[batch.ID], item)
			}
			if executed == "other_failure" {
				item.Reason = "Uncertain execution failure"
				store.items[root] = item
			}
			if executed == "filter_id" {
				item.FilterRunID = "previous-filter"
				store.items[root] = item
			}
			if executed == "observer_run" {
				store.runs["previous-filter"] = model.ControlRun{ID: "previous-filter", BatchID: batch.ID, ItemID: root, State: "interrupted"}
			}
			batch = store.batches[batch.ID]
			batch.Status = "running"
			batch.Attempt++
			svc.process(context.Background(), batch)
			if executed == "none" {
				if repo.creations != 3 || store.items[root].Status != "COMPLETED" || store.batches[batch.ID].Status != "succeeded" {
					t.Fatalf("untouched items not resumed: %+v", store.items[root])
				}
			} else if repo.creations != 0 || store.items[root].Status != "FAILED" || store.batches[batch.ID].Status != "partial" {
				t.Fatalf("executed item replayed: %+v", store.items[root])
			}
		})
	}
}
