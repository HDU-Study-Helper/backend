package runcontrol

import (
	f "academic/agent/framework"
	c "academic/backend/internal/continuation"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
)

func (s *Service) bindContinuation(ctx context.Context, b model.ControlBatch, item string) context.Context {
	if _, ok := s.Agents.Registry.Repository.(c.Store); !ok {
		return ctx
	}
	key := item
	if key == "" {
		key = "batch:" + b.ID
	}
	return c.WithControl(c.WithSource(ctx, b.OwnerID, c.ChA, key), c.ControlLink{Owner: b.OwnerID, Batch: b.ID, Item: item})
}
func (s *Service) pendingItem(ctx context.Context, item *model.ControlItem) (bool, error) {
	if _, ok := s.Agents.Registry.Repository.(c.Store); !ok {
		return false, nil
	}
	qs, e := s.Agents.PendingQuestions(ctx, item.OwnerID, c.ChA, item.ID)
	if e != nil {
		return false, e
	}
	item.PendingQuestions = nil
	for _, q := range qs {
		item.PendingQuestions = append(item.PendingQuestions, q.ID)
	}
	if len(qs) == 0 {
		return false, nil
	}
	item.Status = "NEEDS_USER_INPUT"
	item.Reason = qs[0].Text
	return true, nil
}

// Keep the original batch/round as a historical snapshot. The current item
// receives concrete resumed run IDs. A Filter summary alone is not proof of
// successful execution; require an observed completed Worker for this answer.
func (s *Service) finishContinuation(ctx context.Context, l c.ControlLink, ids []string, out f.RunResult) error {
	if l.Item == "" {
		return nil
	}
	store, ok := s.Store.(model.ControlItemRepository)
	if !ok {
		return nil
	}
	item, e := store.GetItem(ctx, l.Item)
	if e != nil {
		return e
	}
	if item.OwnerID != l.Owner || item.BatchID != l.Batch {
		return f.Fail("PERMISSION_DENIED", "continuation item scope mismatch")
	}
	item.ContinuationRuns = append(item.ContinuationRuns, ids...)
	if waiting, e := s.pendingItem(ctx, &item); e != nil {
		return e
	} else if waiting {
		return store.SaveItem(ctx, item)
	}
	item.Status = "PARTIAL"
	item.Reason = "已接收回复；暂无已核验的事项完成记录"
	runs, e := s.Store.BatchRuns(ctx, l.Batch)
	if e != nil {
		return e
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	workerDone, filterDone := false, false
	for _, r := range runs {
		if !allowed[r.ID] || r.OwnerID != l.Owner || r.ItemID != l.Item || r.State != "succeeded" {
			continue
		}
		a, e := s.Agents.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: l.Owner, AgentID: r.AgentID})
		if e != nil {
			return e
		}
		if a.Type == "worker" && r.WorkflowID != "" && r.BusinessStatus == "COMPLETED" {
			workerDone = true
		}
		if a.Type == "learner" && r.ID == out.RunID && item.Resource == "selected_course" {
			var v struct {
				Status   string
				Verified bool
			}
			_ = json.Unmarshal([]byte(r.Output), &v)
			switch {
			case v.Status == "APPLIED" && v.Verified:
				item.Status = "COMPLETED"
				item.Reason = "回复后已核验课程建档提交"
			case v.Status == "NO_CHANGE":
				item.Status = "IGNORED"
				item.Reason = "回复后核实无需更改"
			}
		}
		if a.Type == "filter" && r.ID == out.RunID {
			rows, err := parseItemResults(r.Output, item.ID)
			filterDone = err == nil && len(rows) == 1 && rows[0].ID == item.ID && rows[0].Status == "COMPLETED"
		}
	}
	if workerDone && filterDone {
		item.Status = "COMPLETED"
		item.Reason = "回复后由原处理链完成，见续跑记录"
	}
	return store.SaveItem(ctx, item)
}
