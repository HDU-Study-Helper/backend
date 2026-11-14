package runcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"academic/backend/internal/model"
)

func groupedTestBatch(n int) model.ControlBatch {
	b := model.ControlBatch{ID: "group-batch", RoundID: "round", OwnerID: "owner", Source: "chaoxing", Stage: "items", Status: "running", Scope: &model.ControlBatchScope{Kind: "course", ConnectionID: "connection", CourseRef: "course", CourseName: "数学分析"}}
	ids := []string{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("change-%03d", i)
		b.Changes = append(b.Changes, model.ControlChange{ID: id, RecordID: fmt.Sprint(i + 10), ConnectionID: "connection", Source: "chaoxing", Resource: "material", After: mustJSON(map[string]any{"external_id": fmt.Sprintf("native-%d", i), "content": "完整原文"}), Before: mustJSON(map[string]any{"content": "旧原文"})})
		ids = append(ids, id)
	}
	b.Trigger = mustJSON(map[string]any{"decision": "TRIGGER", "reason": "有价值的课程变化", "relevant_change_ids": ids})
	return b
}

func TestCourseGroupsBoundInputWithoutDroppingChanges(t *testing.T) {
	svc, _, store, _, _ := newItemsIntegration(t, "")
	b := groupedTestBatch(123)
	units, err := svc.itemWorkUnits(context.Background(), b, b.Changes, store)
	if err != nil || len(units) != 3 {
		t.Fatalf("groups=%d err=%v", len(units), err)
	}
	seen := 0
	for _, unit := range units {
		raw, err := buildWorkItemInput(b, unit)
		if err != nil || len(raw) > courseGroupInputBytes || len(unit.changes) > 50 {
			t.Fatalf("invalid bounded group: %d %v", len(raw), err)
		}
		var payload struct {
			Content []model.ControlChange `json:"content"`
			Refs    []string              `json:"evidence_refs"`
		}
		if err = json.Unmarshal(raw, &payload); err != nil || len(payload.Refs) > 100 || len(payload.Content) != len(unit.changes) {
			t.Fatal("invalid event", err)
		}
		for i, change := range payload.Content {
			original := b.Changes[seen]
			if change.ID != original.ID || string(change.Before) != string(original.Before) || string(change.After) != string(original.After) || payload.Refs[i] != original.ID {
				t.Fatal("lost original evidence")
			}
			seen++
		}
		decoded, err := changesForSavedItem(unit.item)
		if err != nil || len(decoded) != len(unit.changes) {
			t.Fatal("saved envelope", err)
		}
	}
	if seen != len(b.Changes) {
		t.Fatal("missing changes")
	}
	// Size, not only count, must split groups. An oversized original stays intact.
	b = groupedTestBatch(4)
	for i := range b.Changes {
		b.Changes[i].After = mustJSON(map[string]any{"content": strings.Repeat("x", 65000)})
	}
	units, err = svc.itemWorkUnits(context.Background(), b, b.Changes, store)
	if err != nil || len(units) < 2 {
		t.Fatal("byte budget did not split", err)
	}
	b.Changes[0].After = mustJSON(map[string]any{"content": strings.Repeat("x", 140000)})
	units, err = svc.itemWorkUnits(context.Background(), b, b.Changes, store)
	if err != nil || units[0].grouped() || len(units[0].changes[0].After) < 140000 {
		t.Fatal("large single original truncated", err)
	}
}

func TestCourseGroupsKeepLegacyIdentityAndRejectMixedScopes(t *testing.T) {
	svc, _, store, _, _ := newItemsIntegration(t, "")
	b := groupedTestBatch(3)
	for _, mode := range []string{"legacy", "unassociated", "connection", "source", "executed", "observed"} {
		t.Run(mode, func(t *testing.T) {
			candidate := b
			candidate.Changes = append([]model.ControlChange(nil), b.Changes...)
			scope := *b.Scope
			candidate.Scope = &scope
			store.items = map[string]model.ControlItem{}
			store.runs = map[string]model.ControlRun{}
			switch mode {
			case "legacy":
				candidate.Scope = nil
			case "unassociated":
				candidate.Scope.Kind = "unassociated"
			case "connection":
				candidate.Changes[1].ConnectionID = "other"
			case "source":
				candidate.Changes[1].Source = "mooc"
			case "executed":
				item := newControlItem(candidate, candidate.Changes[0])
				item.FilterRunID = "old-filter"
				store.items[item.ID] = item
			case "observed":
				store.runs["old-filter"] = model.ControlRun{ID: "old-filter", BatchID: b.ID, ItemID: stableItemID(b.ID, b.Changes[0].ID)}
			}
			units, err := svc.itemWorkUnits(context.Background(), candidate, candidate.Changes, store)
			if err != nil || len(units) != 3 || units[0].grouped() {
				t.Fatal("unsafe group", err)
			}
		})
	}
}

func TestCourseGroupEvidenceValidatesEachOriginalNamespace(t *testing.T) {
	svc := &Service{}
	b := groupedTestBatch(3)
	for _, raw := range []string{`["change-000","change-002"]`, `[{"kind":"change_id","change_id":"change-001"},"native-2"]`} {
		if !svc.validateWorkUnitEvidence(context.Background(), "owner", json.RawMessage(raw), b.Changes) {
			t.Fatal("valid mixed refs rejected", raw)
		}
	}
	for _, raw := range []string{`["unknown"]`, `[{"kind":"change_id","change_id":"native-2"}]`, `[{"kind":"connection_id","connection_id":"other"}]`} {
		if svc.validateWorkUnitEvidence(context.Background(), "owner", json.RawMessage(raw), b.Changes) {
			t.Fatal("invalid ref accepted", raw)
		}
	}
}

func TestCourseGroupsRealPipelineSharesVerifiedRunAndResumes(t *testing.T) {
	// The existing fixture exercises real runtime, Learner/Worker dispatch,
	// source reads, route checks and artifacts with fake models/persistence.
	svc, repo, store, _, _ := newItemsIntegration(t, "")
	// This fixture explicitly copies the full event through seven dispatches;
	// provision context for that audit history as production must do as well.
	for id, instance := range repo.agents {
		instance.Limits.ContextBytes = 2 << 20
		repo.agents[id] = instance
	}
	b := groupedTestBatch(50)
	// Existing fixture reports notice-change evidence; retain that real ID.
	b.Changes[0].ID = "notice-change"
	ids := []string{}
	for _, change := range b.Changes {
		ids = append(ids, change.ID)
		item := newControlItem(b, change)
		store.items[item.ID] = item
	}
	b.Trigger = mustJSON(map[string]any{"decision": "TRIGGER", "reason": "课程变化", "relevant_change_ids": ids})
	svc.process(context.Background(), b)
	if store.batches[b.ID].Status != "succeeded" {
		t.Fatalf("batch status=%s error=%s filter=%s", store.batches[b.ID].Status, store.batches[b.ID].Error, store.batches[b.ID].Filter)
	}
	filterRuns := 0
	groupID := ""
	for _, run := range store.runs {
		if run.AgentID == "filter" {
			filterRuns++
			groupID = run.ItemID
		}
	}
	if filterRuns != 1 || groupID == "" {
		t.Fatalf("Filter runs=%d", filterRuns)
	}
	for _, change := range b.Changes {
		item := store.items[stableItemID(b.ID, change.ID)]
		if item.Status != "COMPLETED" || item.ParentID != groupID || item.FilterRunID == "" || len(item.Routes) != 2 || string(item.Input) != string(newControlItem(b, change).Input) || string(item.Evidence) != string(mustJSON([]string{change.ID})) {
			t.Fatalf("source audit link lost: %+v", item)
		}
	}
	before := len(repo.runs)
	b.Attempt++
	svc.process(context.Background(), b)
	if len(repo.runs) != before || store.batches[b.ID].Status != "succeeded" {
		t.Fatal("retry replayed verified group")
	}
}

func TestCourseGroupCancellationPreservesRetryIdentity(t *testing.T) {
	svc, _, store, _, _ := newItemsIntegration(t, "")
	b := groupedTestBatch(3)
	units, err := svc.itemWorkUnits(context.Background(), b, b.Changes, store)
	if err != nil {
		t.Fatal(err)
	}
	unit := units[0]
	unit.item.Status = "RUNNING"
	store.items[unit.item.ID] = unit.item
	if err = syncCourseGroupSources(context.Background(), b, unit, store, "RUNNING"); err != nil {
		t.Fatal(err)
	}
	if err = syncCourseGroupSources(context.Background(), b, unit, store, ""); err != nil {
		t.Fatal(err)
	}
	if store.items[unit.item.ID].Status != "FAILED" {
		t.Fatal("group left running")
	}
	pinned, err := svc.itemWorkUnits(context.Background(), b, b.Changes, store)
	if err != nil || len(pinned) != 1 || pinned[0].item.ID != unit.item.ID {
		t.Fatal("lost retry identity", err)
	}
	for _, change := range b.Changes {
		if store.items[stableItemID(b.ID, change.ID)].Status != "FAILED" {
			t.Fatal("source left pending")
		}
	}
}

func TestCourseGroupPartialRetryKeepsRemainingGroupsBounded(t *testing.T) {
	svc, _, store, _, _ := newItemsIntegration(t, "")
	b := groupedTestBatch(123)
	units, err := svc.itemWorkUnits(context.Background(), b, b.Changes, store)
	if err != nil {
		t.Fatal(err)
	}
	first := units[0]
	first.item.Status = "FAILED"
	first.item.Reason = "处理未完成，请查看批次与运行错误"
	store.items[first.item.ID] = first.item
	if err = syncCourseGroupSources(context.Background(), b, first, store, ""); err != nil {
		t.Fatal(err)
	}
	resumed, err := svc.itemWorkUnits(context.Background(), b, b.Changes, store)
	if err != nil || len(resumed) != len(units) {
		t.Fatalf("resumed groups=%d err=%v", len(resumed), err)
	}
	for i := range units {
		if resumed[i].item.ID != units[i].item.ID {
			t.Fatal("group identity changed")
		}
	}
}

func TestCourseGroupReconciliationRefreshesSourceAuditWithoutReplay(t *testing.T) {
	svc, repo, rawStore, _, _ := newItemsIntegration(t, "")
	store := &academicReconcileStore{rawStore}
	svc.Store = store
	b := groupedTestBatch(3)
	b.Changes[0].ID = "notice-change"
	ids := []string{}
	for _, change := range b.Changes {
		ids = append(ids, change.ID)
	}
	b.Trigger = mustJSON(map[string]any{"decision": "TRIGGER", "reason": "课程变化", "relevant_change_ids": ids})
	svc.process(context.Background(), b)
	if store.batches[b.ID].Status != "succeeded" {
		t.Fatal("setup failed")
	}
	var filter model.ControlRun
	for _, run := range store.runs {
		if run.AgentID == "filter" {
			filter = run
		}
	}
	parsed, err := parseItemResults(filter.Output, filter.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	child := store.items[parsed[0].ID]
	child.Status = "PARTIAL"
	child.Reason = parsed[0].Reason + "；证据引用不属于当前输入事项"
	store.items[child.ID] = child
	parent := store.items[filter.ItemID]
	parent.Status = "PARTIAL"
	store.items[parent.ID] = parent
	unit := itemWorkUnit{item: parent, changes: b.Changes}
	if err = syncCourseGroupSources(context.Background(), b, unit, store, ""); err != nil {
		t.Fatal(err)
	}
	before := len(repo.runs)
	changed, err := svc.ReconcileSavedItemResults(context.Background(), filter.ID)
	if err != nil || changed != 1 || len(repo.runs) != before {
		t.Fatalf("reconcile=%d err=%v", changed, err)
	}
	for _, change := range b.Changes {
		if store.items[stableItemID(b.ID, change.ID)].Status != "COMPLETED" {
			t.Fatal("source audit stale")
		}
	}
}

func TestCourseGroupFailedChildNeverCompletesSourceAudit(t *testing.T) {
	svc, _, store, _, _ := newItemsIntegration(t, "fabricated_run")
	b := groupedTestBatch(3)
	b.Changes[0].ID = "notice-change"
	ids := []string{}
	for _, change := range b.Changes {
		ids = append(ids, change.ID)
	}
	b.Trigger = mustJSON(map[string]any{"decision": "TRIGGER", "reason": "课程变化", "relevant_change_ids": ids})
	svc.process(context.Background(), b)
	if store.batches[b.ID].Status != "partial" {
		t.Fatal("invalid child route accepted")
	}
	for _, change := range b.Changes {
		item := store.items[stableItemID(b.ID, change.ID)]
		if item.Status != "PARTIAL" || item.ParentID == "" || item.FilterRunID == "" {
			t.Fatalf("false source completion: %s", item.Status)
		}
	}
}
