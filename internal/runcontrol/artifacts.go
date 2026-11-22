package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
)

// Capture completed child results immediately, independently of Filter's final JSON.
// The stable parent item plus run ID is the artifact identity; re-observation is idempotent.
func (s *Service) captureTerminalArtifacts(ctx context.Context, store model.ControlItemRepository, item model.ControlItem, run model.ControlRun, role string) {
	if role == "worker" {
		s.captureRunArtifacts(ctx, store, item, run)
		return
	}
	if role != "learner" {
		return
	}
	var out struct {
		WorkflowID string `json:"workflow_id"`
		Verified   bool   `json:"verified"`
		Receipt    *struct {
			WorkflowID string            `json:"workflow_id"`
			IDMap      map[string]string `json:"id_map"`
			Verified   bool              `json:"verified"`
		} `json:"commit_receipt"`
		Results []struct {
			WorkflowID string `json:"workflow_id"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(run.Output), &out) != nil {
		return
	}
	if item.Source == "first_run" {
		if !out.Verified || out.Receipt == nil || !out.Receipt.Verified || out.Receipt.WorkflowID != out.WorkflowID {
			return
		}
		var executions []f.ToolExecution
		_ = json.Unmarshal(run.ToolExecutions, &executions)
		for _, execution := range executions {
			if execution.Error != nil || (execution.Name != "memory_create" && execution.Name != "memory_update") {
				continue
			}
			for _, ref := range execution.ResultRefs {
				id := out.Receipt.IDMap[ref.ID]
				if ref.Kind != "MEMORY" || id == "" {
					continue
				}
				m, err := s.Agents.Registry.Repository.GetMemory(ctx, f.Scope{OwnerID: item.OwnerID, AgentID: run.AgentID, WorkflowID: out.WorkflowID}, id)
				if err == nil && m.WorkflowID == out.WorkflowID {
					_ = store.SaveArtifact(ctx, model.ControlArtifact{ID: artifactID(item.ID, run.ID, "memory:"+id, 0), ItemID: item.ID, BatchID: item.BatchID, RoundID: item.RoundID, RunID: run.ID, Type: "MEMORY", Title: m.Title, Description: m.Content, EntityRef: id, Data: mustJSON(m)})
				}
			}
		}
	}
	ids := []string{}
	if out.WorkflowID != "" {
		ids = append(ids, out.WorkflowID)
	}
	for _, result := range out.Results {
		if result.WorkflowID != "" {
			ids = append(ids, result.WorkflowID)
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		w, e := s.Agents.Registry.Repository.GetWorkflow(ctx, item.OwnerID, id)
		if e != nil {
			continue
		}
		_ = store.SaveArtifact(ctx, model.ControlArtifact{ID: artifactID(item.ID, run.ID, "workflow:"+id, 0), ItemID: item.ID, BatchID: item.BatchID, RoundID: item.RoundID, RunID: run.ID, Type: "WORKFLOW", Title: w.Name, Description: "Learner真实工作流结果（已回读）", EntityRef: w.ID, Data: mustJSON(w)})
	}
}
func (s *Service) BackfillArtifacts(ctx context.Context, batchID string) error {
	store, ok := s.Store.(model.ControlItemRepository)
	if !ok {
		return model.ErrUnsupported
	}
	runs, e := s.Store.BatchRuns(ctx, batchID)
	if e != nil {
		return e
	}
	for _, run := range runs {
		if run.ItemID == "" {
			continue
		}
		item, e := store.GetItem(ctx, run.ItemID)
		if e != nil {
			continue
		}
		s.capturePersistedToolArtifacts(ctx, store, item, run)
		a, e := s.Agents.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: run.OwnerID, AgentID: run.AgentID})
		if e != nil {
			continue
		}
		s.captureTerminalArtifacts(ctx, store, item, run, a.Type)
	}
	return nil
}
