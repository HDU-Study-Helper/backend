package runcontrol

import (
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"testing"
)

type artifactStore struct {
	model.ControlItemRepository
	values map[string]model.ControlArtifact
}

func (s *artifactStore) SaveArtifact(_ context.Context, v model.ControlArtifact) error {
	s.values[v.ID] = v
	return nil
}
func TestWorkerArtifactsAvailableBeforeFilterFinalAndIdempotent(t *testing.T) {
	store := &artifactStore{values: map[string]model.ControlArtifact{}}
	service := &Service{}
	item := model.ControlItem{ID: "root", BatchID: "batch", RoundID: "round", Title: "具体任务"}
	run := model.ControlRun{ID: "worker-run", Output: `{"status":"NEEDS_USER_INPUT","artifacts":[{"type":"TASK","title":"高数报告","description":"需确认截止时间","entity_ref":null,"data":{"pending":true}}]}`}
	service.captureTerminalArtifacts(context.Background(), store, item, run, "worker")
	service.captureTerminalArtifacts(context.Background(), store, item, run, "worker")
	if len(store.values) != 1 {
		t.Fatal(store.values)
	}
	for _, v := range store.values {
		if v.RunID != run.ID || v.ItemID != item.ID || !json.Valid(v.Data) {
			t.Fatal(v)
		}
	}
}
