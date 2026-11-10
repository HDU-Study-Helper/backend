package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
)

// ReconcileSavedItemResults revalidates only the former overly narrow evidence
// rejection. It never invokes a model or replays tools. Raw Run outputs stay intact.
func (s *Service) ReconcileSavedItemResults(ctx context.Context, filterRunID string) (int, error) {
	store, ok := s.Store.(model.ControlItemRepository)
	if !ok {
		return 0, model.ErrUnsupported
	}
	run, e := s.Store.GetRun(ctx, filterRunID)
	if e != nil {
		return 0, e
	}
	if run.ItemID == "" || run.State != "succeeded" || run.Output == "" {
		return 0, model.ErrInvalidArgument
	}
	actor, e := s.Agents.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: run.OwnerID, AgentID: run.AgentID})
	if e != nil {
		return 0, e
	}
	if actor.Type != "filter" {
		return 0, model.ErrInvalidArgument
	}
	parent, e := store.GetItem(ctx, run.ItemID)
	if e != nil {
		return 0, e
	}
	if parent.OwnerID != run.OwnerID || parent.BatchID != run.BatchID {
		return 0, model.ErrConflict
	}
	var change model.ControlChange
	if json.Unmarshal(parent.Input, &change) != nil || change.ID != parent.ChangeID {
		return 0, model.ErrInvalidArgument
	}
	batch, e := s.Store.GetBatch(ctx, parent.BatchID)
	if e != nil {
		return 0, e
	}
	parsed, e := parseItemResults(run.Output, parent.ID)
	if e != nil {
		return 0, e
	}
	runs, e := s.Store.BatchRuns(ctx, parent.BatchID)
	if e != nil {
		return 0, e
	}
	actual := map[string]model.ControlRun{}
	for _, child := range runs {
		if child.ParentID == run.ID {
			actual[child.ID] = child
		}
	}
	itemContext := s.enrichItemContext(ctx, batch, change)
	// Coverage and route identity are checked before any result is changed.
	reported := map[string]bool{}
	for _, out := range parsed {
		for _, route := range out.Routes {
			if recoveredPreflightFailure(route, out.Routes, actual) {
				continue
			}
			if route.Execution == nil {
				return 0, model.ErrConflict
			}
			if route.Execution.RunID == "" && route.Execution.Error != nil && out.Status != "COMPLETED" {
				continue
			}
			child, exists := actual[route.Execution.RunID]
			if !exists || reported[child.ID] || child.OwnerID != run.OwnerID || child.State != "succeeded" {
				return 0, model.ErrConflict
			}
			reported[child.ID] = true
			target, err := s.Agents.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: run.OwnerID, AgentID: child.AgentID})
			if err != nil || !strings.EqualFold(target.Type, route.Target) {
				return 0, model.ErrConflict
			}
			if route.Target == "WORKER" {
				if child.WorkflowID != route.WorkflowID {
					return 0, model.ErrConflict
				}
				workflow, err := s.Agents.Registry.Repository.GetWorkflow(ctx, run.OwnerID, route.WorkflowID)
				if err != nil || workflow.Status != "active" || !validEntityWorkflow(workflow.Definition) {
					return 0, model.ErrConflict
				}
			} else if route.Target == "LEARNER" {
				_, verified, err := s.verifyLearnerResult(ctx, run.OwnerID, route, child)
				if err != nil {
					return 0, err
				}
				route.Execution.ServerVerified = true
				route.Execution.ModelVerified = verified
			} else {
				return 0, model.ErrConflict
			}
		}
	}
	if len(reported) != len(actual) {
		return 0, model.ErrConflict
	}
	changed := 0
	for _, out := range parsed {
		current, err := store.GetItem(ctx, out.ID)
		if err != nil {
			return changed, err
		}
		formattingOnly := current.FilterRunID == run.ID && current.Status == "FAILED" && current.Reason == "Filter必须返回非空items，逐项说明处理结果"
		itemIDOnly := current.FilterRunID == run.ID && current.Status == "FAILED" && current.Reason == "item_id、父子归属或结果状态无效" && out.IDNormalized
		learnerFlagOnly := current.FilterRunID == run.ID && current.Status == "PARTIAL" && out.Status != "PARTIAL" && current.Reason == out.Reason+"；Learner未返回已核验的创建或复用结果"
		if !evidenceOnlyRejection(current, out, run.ID) && !formattingOnly && !itemIDOnly && !learnerFlagOnly {
			continue
		}
		if !s.validateItemEvidence(itemContext, run.OwnerID, out.Evidence, change) {
			continue
		}
		if out.Status == "COMPLETED" {
			complete := false
			for _, route := range out.Routes {
				if route.Target == "WORKER" && route.Execution != nil {
					child, ok := actual[route.Execution.RunID]
					complete = complete || (ok && child.BusinessStatus == "COMPLETED")
				}
			}
			if !complete {
				continue
			}
		}
		current.Status = out.Status
		current.ReportedID = out.ReportedID
		current.IDNormalized = out.IDNormalized
		current.Reason = out.Reason
		current.Evidence = out.Evidence
		current.Routes = mustJSON(out.Routes)
		if err = store.SaveItem(ctx, current); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}
func evidenceOnlyRejection(current model.ControlItem, out itemResult, runID string) bool {
	return current.FilterRunID == runID && current.Status == "PARTIAL" && out.Status != "PARTIAL" && current.Reason == out.Reason+"；证据引用不属于当前输入事项"
}
