package runcontrol

import (
	"context"
	"encoding/json"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

// capturePersistedToolArtifacts observes trusted tool result IDs. It does not
// infer writes from model output, timestamps or other agents' memory lists.
func (s *Service) capturePersistedToolArtifacts(ctx context.Context, store model.ControlItemRepository, item model.ControlItem, run model.ControlRun) {
	if s.Agents == nil || run.OwnerID != item.OwnerID || run.AgentID == "" {
		return
	}
	var executions []f.ToolExecution
	if json.Unmarshal(run.ToolExecutions, &executions) != nil {
		return
	}
	seen := map[string]bool{}
	for _, execution := range executions {
		if execution.Error != nil {
			continue
		}
		for _, ref := range execution.ResultRefs {
			if kind := map[string]string{"TASK": "todo", "NOTICE": "notice", "PLANNED_JOB": "job"}[ref.Kind]; kind != "" && ref.ID != "" && s.Actions != nil {
				key := kind + ":" + ref.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				a, err := s.Actions.GetAction(ctx, run.OwnerID, kind, ref.ID)
				if err != nil {
					continue
				}
				title := a.Input.Title
				if title == "" {
					title = a.Input.Kind + "计划任务"
				}
				_ = store.SaveArtifact(ctx, model.ControlArtifact{ID: artifactID(item.ID, run.ID, key, 0), ItemID: item.ID, BatchID: item.BatchID, RoundID: item.RoundID, RunID: run.ID, Type: ref.Kind, Title: title, Description: a.Input.Body, EntityRef: a.ID, Data: mustJSON(a)})
				continue
			}
			if (execution.Name != "memory_create" && execution.Name != "memory_update") || ref.Kind != "MEMORY" || ref.ID == "" || seen[ref.ID] {
				continue
			}
			seen[ref.ID] = true
			memory, err := s.Agents.Registry.Repository.GetMemory(ctx, f.Scope{OwnerID: run.OwnerID, AgentID: run.AgentID, WorkflowID: run.WorkflowID}, ref.ID)
			if err != nil {
				continue
			}
			_ = store.SaveArtifact(ctx, model.ControlArtifact{ID: artifactID(item.ID, run.ID, "memory:"+memory.ID, 0), ItemID: item.ID, BatchID: item.BatchID, RoundID: item.RoundID, RunID: run.ID, Type: "MEMORY", Title: memory.Title, Description: memory.Content, EntityRef: memory.ID, Data: mustJSON(memory)})
		}
	}
}
