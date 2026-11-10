package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
)

// A missing model flag is distinct from explicit false. Host verification records
// real persisted owner/entity/version facts without rewriting the model's output.
func (s *Service) verifyLearnerResult(ctx context.Context, owner string, route itemRoute, run model.ControlRun) (f.Workflow, *bool, error) {
	var applied struct {
		Status   string          `json:"status"`
		ID       string          `json:"workflow_id"`
		Version  int             `json:"workflow_version"`
		Verified *bool           `json:"verified"`
		Entity   json.RawMessage `json:"entity"`
	}
	if json.Unmarshal([]byte(run.Output), &applied) != nil || applied.ID == "" || (applied.Status != "APPLIED" && applied.Status != "NO_CHANGE") || (applied.Status == "APPLIED" && applied.Verified != nil && !*applied.Verified) {
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
