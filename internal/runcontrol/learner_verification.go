package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
)

type learnerResult struct {
	Status   string          `json:"status"`
	ID       string          `json:"workflow_id"`
	Version  int             `json:"workflow_version"`
	Verified *bool           `json:"verified"`
	Entity   json.RawMessage `json:"entity"`
}

func selectLearnerResult(output string, workflowID string) (learnerResult, error) {
	var payload struct {
		learnerResult
		Results json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return payload.learnerResult, f.Fail("INVALID_LEARNER_RESULT", "Learner未返回已核验的创建或复用结果")
	}
	if len(payload.Results) == 0 {
		return payload.learnerResult, nil
	}
	var results []*learnerResult
	if json.Unmarshal(payload.Results, &results) != nil || len(results) == 0 {
		return learnerResult{}, f.Fail("INVALID_LEARNER_RESULT", "Learner results必须为非空结果数组")
	}
	var selected *learnerResult
	for _, result := range results {
		if result == nil || result.ID == "" {
			return learnerResult{}, f.Fail("INVALID_LEARNER_RESULT", "Learner results包含无效结果")
		}
		if workflowID == "" || result.ID == workflowID {
			if selected != nil {
				return learnerResult{}, f.Fail("INVALID_LEARNER_RESULT", "Learner results存在歧义，无法唯一匹配Workflow")
			}
			selected = result
		}
	}
	if selected == nil {
		return learnerResult{}, f.Fail("INVALID_LEARNER_RESULT", "Learner路由与实际Workflow不匹配")
	}
	return *selected, nil
}

// A missing model flag is distinct from explicit false. Host verification records
// real persisted owner/entity/version facts without rewriting the model's output.
func (s *Service) verifyLearnerResult(ctx context.Context, owner string, route itemRoute, run model.ControlRun) (f.Workflow, *bool, error) {
	applied, err := selectLearnerResult(run.Output, route.WorkflowID)
	if err != nil {
		return f.Workflow{}, applied.Verified, err
	}
	if applied.ID == "" || (applied.Status != "APPLIED" && applied.Status != "NO_CHANGE") || (applied.Status == "APPLIED" && applied.Verified != nil && !*applied.Verified) {
		return f.Workflow{}, applied.Verified, f.Fail("INVALID_LEARNER_RESULT", "Learner未返回已核验的创建或复用结果")
	}
	if route.WorkflowID != "" && route.WorkflowID != applied.ID {
		return f.Workflow{}, applied.Verified, f.Fail("INVALID_LEARNER_RESULT", "Learner路由与实际Workflow不匹配")
	}
	w, err := s.Agents.Registry.Repository.GetWorkflow(ctx, owner, applied.ID)
	if err != nil {
		return w, applied.Verified, err
	}
	if w.OwnerID != owner || w.Status != "active" || !validEntityWorkflow(w.Definition) || applied.Version < 1 || applied.Version != w.Version {
		return w, applied.Verified, f.Fail("INVALID_LEARNER_RESULT", "Learner结果Workflow归属、实体、状态或版本未通过宿主核验")
	}
	if len(applied.Entity) > 0 && string(applied.Entity) != "null" {
		var expected, actual struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		var definition struct {
			Entity json.RawMessage `json:"entity"`
		}
		if json.Unmarshal(applied.Entity, &expected) != nil || json.Unmarshal(w.Definition, &definition) != nil || json.Unmarshal(definition.Entity, &actual) != nil || expected.Kind != actual.Kind || expected.ID != actual.ID || expected.Name != actual.Name {
			return w, applied.Verified, f.Fail("INVALID_LEARNER_RESULT", "Learner返回实体与持久化Workflow不一致")
		}
	}
	if err := s.verifyWorkflowProfile(ctx, owner, w); err != nil {
		return w, applied.Verified, err
	}
	return w, applied.Verified, nil
}
