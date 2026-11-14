package runcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"academic/backend/internal/model"
)

// Storage batches can be much larger than one model request. Grouping only
// changes execution granularity; every original change and source item survives.
const courseGroupEvents = 50
const courseGroupInputBytes = 128 << 10
const courseGroupContract = "处理完整课程变化组，按变化ID逐条溯源；结合全部原文识别有价值的业务事项，允许拆成独立items子项，不必每条记录单独调用或重复查询整课。item_id用父ID或父ID:1/:2。每项必须COMPLETED/IGNORED/NEEDS_USER_INPUT/PARTIAL/FAILED，routes绑定item_id，所有实际子调用均须报告。"

type itemWorkUnit struct {
	item    model.ControlItem
	changes []model.ControlChange
}

type courseGroupInput struct {
	Kind    string                   `json:"processing_unit"`
	Scope   *model.ControlBatchScope `json:"scope"`
	Changes []model.ControlChange    `json:"changes"`
}

func (u itemWorkUnit) grouped() bool { return u.item.Resource == "course_change_group" }

func buildWorkItemInput(b model.ControlBatch, unit itemWorkUnit) (json.RawMessage, error) {
	input, err := filterInput(b, unit.changes)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err = json.Unmarshal(input, &payload); err != nil {
		return nil, err
	}
	payload["item_id"] = unit.item.ID
	payload["processing_contract"] = "逐项处理此change；原文多个课程或事务拆成items子项。item_id用父ID或父ID:1/:2。每项必须COMPLETED/IGNORED/NEEDS_USER_INPUT/PARTIAL/FAILED。routes绑定item_id，所有实际子调用均须报告。"
	if unit.grouped() {
		payload["processing_contract"] = courseGroupContract
		payload["processing_unit"] = "course_change_group"
		// The original Trigger stays on the immutable batch. Forward its verified
		// decision and this group's selected IDs rather than repeating thousands
		// of unrelated IDs in every bounded unit.
		var trigger triggerDecision
		if json.Unmarshal(b.Trigger, &trigger) == nil {
			trigger.IDs = make([]string, len(unit.changes))
			for i, change := range unit.changes {
				trigger.IDs[i] = change.ID
			}
			if context, ok := payload["context"].(map[string]any); ok {
				context["trigger"] = trigger
				context["trigger_scope"] = "current_course_change_group"
				context["full_trigger_batch_id"] = b.ID
			}
		}
	}
	return json.Marshal(payload)
}

func newCourseGroup(b model.ControlBatch, changes []model.ControlChange) itemWorkUnit {
	ids := make([]string, len(changes))
	for i, change := range changes {
		ids[i] = change.ID
	}
	// JSON encoding avoids ambiguous concatenations of external identifiers.
	id := stableItemID(b.ID, "course-group/"+string(mustJSON(ids)))
	title := b.Scope.CourseName
	if title == "" {
		title = b.Scope.CourseRef
	}
	item := model.ControlItem{ID: id, BatchID: b.ID, RoundID: b.RoundID, OwnerID: b.OwnerID, Source: b.Source, Resource: "course_change_group", Title: fmt.Sprintf("%s：%d条课程变化", title, len(changes)), Status: "PENDING", Routes: mustJSON([]any{}), Evidence: mustJSON(ids), Input: mustJSON(courseGroupInput{Kind: "course_change_group", Scope: b.Scope, Changes: changes})}
	return itemWorkUnit{item: item, changes: changes}
}

func (s *Service) itemWorkUnits(ctx context.Context, b model.ControlBatch, changes []model.ControlChange, store model.ControlItemRepository) ([]itemWorkUnit, error) {
	legacy := func() []itemWorkUnit {
		units := make([]itemWorkUnit, 0, len(changes))
		for _, change := range changes {
			units = append(units, itemWorkUnit{item: newControlItem(b, change), changes: []model.ControlChange{change}})
		}
		return units
	}
	if !courseBatch(b) || b.Scope.Kind != "course" || b.Scope.CourseRef == "" || b.Scope.ConnectionID == "" || len(changes) < 2 {
		return legacy(), nil
	}
	// Persisted parent links pin the grouping across retries and future planner
	// changes. Never move an executed source row into a newly computed group.
	pinned := []itemWorkUnit{}
	covered := map[string]bool{}
	for _, change := range changes {
		prior, err := store.GetItem(ctx, stableItemID(b.ID, change.ID))
		if err != nil && err != model.ErrNotFound {
			return nil, err
		}
		if err != nil || prior.ParentID == "" || covered[prior.ParentID] {
			continue
		}
		parent, err := store.GetItem(ctx, prior.ParentID)
		if err != nil {
			return nil, err
		}
		if parent.Resource != "course_change_group" {
			return legacy(), nil
		}
		originals, err := changesForSavedItem(parent)
		if err != nil {
			return nil, err
		}
		pinned = append(pinned, itemWorkUnit{item: parent, changes: originals})
		covered[parent.ID] = true
		for _, original := range originals {
			covered["change/"+original.ID] = true
		}
	}
	if len(pinned) > 0 {
		remaining := []model.ControlChange{}
		for _, change := range changes {
			if !covered["change/"+change.ID] {
				remaining = append(remaining, change)
			}
		}
		if len(remaining) > 0 {
			rest, err := s.itemWorkUnits(ctx, b, remaining, store)
			if err != nil {
				return nil, err
			}
			pinned = append(pinned, rest...)
		}
		return pinned, nil
	}
	for _, change := range changes {
		if change.Source != b.Source || change.ConnectionID != b.Scope.ConnectionID {
			return legacy(), nil
		}
		// Preserve the execution identity of pre-existing per-change results.
		if prior, err := store.GetItem(ctx, stableItemID(b.ID, change.ID)); err == nil && prior.ParentID == "" && (prior.FilterRunID != "" || validItemStatus(prior.Status) && prior.Status != "FAILED" || prior.Status == "FAILED" && prior.Reason != "处理未完成，请查看批次与运行错误") {
			return legacy(), nil
		} else if err != nil && err != model.ErrNotFound {
			return nil, err
		}
	}
	runs, err := s.Store.BatchRuns(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		for _, change := range changes {
			if run.ItemID == stableItemID(b.ID, change.ID) {
				return legacy(), nil
			}
		}
	}
	units := []itemWorkUnit{}
	for start := 0; start < len(changes); {
		end := start
		for end < len(changes) && end-start < courseGroupEvents {
			candidate := newCourseGroup(b, changes[start:end+1])
			raw, err := buildWorkItemInput(b, candidate)
			if err != nil {
				return nil, err
			}
			var event struct {
				Evidence []json.RawMessage `json:"evidence_refs"`
			}
			if err = json.Unmarshal(raw, &event); err != nil {
				return nil, err
			}
			if len(raw) > courseGroupInputBytes || len(event.Evidence) > 100 {
				break
			}
			end++
		}
		if end-start < 2 {
			// A large single original retains the pre-existing single-item path;
			// never truncate it to fit a group.
			change := changes[start]
			units = append(units, itemWorkUnit{item: newControlItem(b, change), changes: []model.ControlChange{change}})
			start++
			continue
		}
		units = append(units, newCourseGroup(b, changes[start:end]))
		start = end
	}
	return units, nil
}

// Validate each reference against the original changes independently: a mixed
// reference list may legitimately cite several records, with strict namespaces.
func (s *Service) validateWorkUnitEvidence(ctx context.Context, owner string, raw json.RawMessage, changes []model.ControlChange) bool {
	if len(raw) == 0 {
		return true
	}
	var refs []json.RawMessage
	if json.Unmarshal(raw, &refs) != nil {
		return false
	}
	for _, ref := range refs {
		valid := false
		for _, change := range changes {
			if s.validateItemEvidence(ctx, owner, mustJSON([]json.RawMessage{ref}), change) {
				valid = true
				break
			}
		}
		if !valid {
			return false
		}
	}
	return true
}

// Source rows are audit links to a group outcome, not additional executions.
// Keep their own original input/evidence and avoid duplicating routes/artifacts.
func syncCourseGroupSources(ctx context.Context, b model.ControlBatch, unit itemWorkUnit, store model.ControlItemRepository, status string) error {
	group, err := store.GetItem(ctx, unit.item.ID)
	if err != nil {
		return err
	}
	if status != "" {
		group.Status = status
	} else if group.Status == "RUNNING" || group.Status == "PENDING" {
		group.Status = "FAILED"
		group.Reason = "处理未完成，请查看批次与运行错误"
		if err = store.SaveItem(ctx, group); err != nil {
			return err
		}
	}
	for _, change := range unit.changes {
		item, err := store.GetItem(ctx, stableItemID(b.ID, change.ID))
		if err == model.ErrNotFound {
			item = newControlItem(b, change)
		} else if err != nil {
			return err
		}
		item.ParentID = group.ID
		item.Status = group.Status
		item.FilterRunID = group.FilterRunID
		item.Reason = strings.TrimSpace("随课程变化组 " + group.ID + " 处理；该组结果：" + group.Status + "；" + group.Reason)
		if err = store.SaveItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func changesForSavedItem(item model.ControlItem) ([]model.ControlChange, error) {
	if item.Resource == "course_change_group" {
		var group courseGroupInput
		if json.Unmarshal(item.Input, &group) != nil || group.Kind != "course_change_group" || len(group.Changes) < 2 || group.Scope == nil || group.Scope.Kind != "course" || group.Scope.CourseRef == "" {
			return nil, model.ErrInvalidArgument
		}
		for _, change := range group.Changes {
			if change.ID == "" || change.Source != item.Source || change.ConnectionID != group.Scope.ConnectionID {
				return nil, model.ErrInvalidArgument
			}
		}
		return group.Changes, nil
	}
	var change model.ControlChange
	if json.Unmarshal(item.Input, &change) != nil || change.ID != item.ChangeID {
		return nil, model.ErrInvalidArgument
	}
	return []model.ControlChange{change}, nil
}
