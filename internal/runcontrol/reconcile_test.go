package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
	"context"
	"testing"
)

type academicReconcileStore struct{ *itemsIntegrationStore }

func (s *academicReconcileStore) GetRun(_ context.Context, id string) (model.ControlRun, error) {
	return s.runs[id], nil
}
func (s *academicReconcileStore) GetBatch(_ context.Context, id string) (model.ControlBatch, error) {
	return s.batches[id], nil
}

func TestReconcileSavedAcademicEvidenceWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name, records, status string
		refs                  []any
		suffix                string
		want                  int
	}{
		{name: "typed completed", records: `["10"]`, status: "COMPLETED", refs: []any{map[string]string{"type": "academic_item_id", "id": academicEvidenceID}}, want: 1},
		{name: "bare needs input", records: `["10"]`, status: "NEEDS_USER_INPUT", refs: []any{academicEvidenceID}, want: 1},
		{name: "prefix completed", records: `["10"]`, status: "COMPLETED", refs: []any{"academic_items.id=" + academicEvidenceID}, want: 1},
		{name: "unrelated record", records: `["11"]`, status: "COMPLETED", refs: []any{academicEvidenceID}},
		{name: "workflow remains rejected", records: `["10"]`, status: "COMPLETED", refs: []any{"workflow"}},
		{name: "route issue remains rejected", records: `["10"]`, status: "COMPLETED", refs: []any{academicEvidenceID}, suffix: "；路由重复或未匹配真实子运行"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			change := model.ControlChange{ID: "change", RecordID: "10", ConnectionID: "connection", Source: "chaoxing"}
			out := itemResult{ID: "item", Status: tc.status, Reason: "verified source", Evidence: mustJSON(tc.refs), Routes: []itemRoute{{ItemID: "item", Target: "WORKER", WorkflowID: "workflow", Execution: &routeExecution{RunID: "worker-run"}}}}
			filter := model.ControlRun{ID: "filter-run", ItemID: "item", BatchID: "batch", OwnerID: "owner", AgentID: "filter", State: "succeeded", Output: string(mustJSON(map[string]any{"items": []itemResult{out}}))}
			store := &academicReconcileStore{&itemsIntegrationStore{batches: map[string]model.ControlBatch{"batch": {ID: "batch", OwnerID: "owner"}}, items: map[string]model.ControlItem{"item": {ID: "item", OwnerID: "owner", BatchID: "batch", ChangeID: "change", FilterRunID: filter.ID, Status: "PARTIAL", Reason: out.Reason + "；证据引用不属于当前输入事项" + tc.suffix, Input: mustJSON(change)}}, runs: map[string]model.ControlRun{filter.ID: filter, "worker-run": {ID: "worker-run", ParentID: filter.ID, BatchID: "batch", OwnerID: "owner", AgentID: "worker", WorkflowID: "workflow", State: "succeeded", BusinessStatus: "COMPLETED"}}}}
			repo := &itemsIntegrationRepo{agents: map[string]f.AgentInstance{"filter": itemsAgent("filter", nil, nil), "worker": itemsAgent("worker", nil, nil)}, workflows: map[string]f.Workflow{"workflow": {ID: "workflow", OwnerID: "owner", Status: "active", Definition: mustJSON(map[string]any{"entity": map[string]string{"kind": "course", "id": "course/academic-analysis", "name": "数学分析A班"}})}}}
			d := &academicEnrichmentData{rows: []map[string]any{{"id": academicEvidenceID, "connection_id": "connection", "record_ids": tc.records}}}
			s := &Service{Store: store, Data: d, Agents: &agent.Service{Registry: f.Registry{Repository: repo}}}
			changed, err := s.ReconcileSavedItemResults(context.Background(), filter.ID)
			if err != nil || changed != tc.want {
				t.Fatalf("changed=%d want=%d err=%v", changed, tc.want, err)
			}
			current, _ := store.GetItem(context.Background(), "item")
			if tc.want == 1 && (current.Status != out.Status || current.Reason != out.Reason) {
				t.Fatal("saved status not recovered", current)
			}
			if tc.want == 0 && current.Status != "PARTIAL" {
				t.Fatal("unproven rejection changed", current)
			}
			if store.runs[filter.ID].Output != filter.Output || len(store.runs) != 2 {
				t.Fatal("saved runs changed or replayed")
			}
			if tc.want == 1 {
				changed, err = s.ReconcileSavedItemResults(context.Background(), filter.ID)
				if err != nil || changed != 0 {
					t.Fatalf("reconciliation not idempotent: %d %v", changed, err)
				}
			}
		})
	}
}

func TestReconcileOnlyFormerEvidenceRejection(t *testing.T) {
	out := itemResult{Status: "COMPLETED", Reason: "真实核查完成"}
	current := model.ControlItem{FilterRunID: "run", Status: "PARTIAL", Reason: out.Reason + "；证据引用不属于当前输入事项"}
	if !evidenceOnlyRejection(current, out, "run") {
		t.Fatal("evidence-only item not eligible")
	}
	current.Reason += "；路由重复或未匹配真实子运行"
	if evidenceOnlyRejection(current, out, "run") {
		t.Fatal("route ambiguity eligible")
	}
	current.Reason = out.Reason + "；证据引用不属于当前输入事项"
	if evidenceOnlyRejection(current, out, "other-run") {
		t.Fatal("different run eligible")
	}
}
