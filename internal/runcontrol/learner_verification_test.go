package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"testing"
)

type learnerVerificationRepo struct {
	f.Repository
	w f.Workflow
}

func (r learnerVerificationRepo) GetWorkflow(context.Context, string, string) (f.Workflow, error) {
	return r.w, nil
}
func TestHostLearnerVerificationDistinguishesMissingAndExplicitFalse(t *testing.T) {
	w := f.Workflow{ID: "wf", OwnerID: "owner", Version: 1, Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"course/class","name":"工科数学分析"}}`)}
	s := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: learnerVerificationRepo{w: w}}}}
	route := itemRoute{Target: "LEARNER"}
	for _, flag := range []string{"", `,"verified":true`} {
		run := model.ControlRun{Output: `{"status":"APPLIED","workflow_id":"wf","workflow_version":1` + flag + `}`}
		got, verified, e := s.verifyLearnerResult(context.Background(), "owner", route, run)
		if e != nil || got.ID != "wf" || (flag == "" && verified != nil) {
			t.Fatal(got, verified, e)
		}
	}
	for _, raw := range []string{`{"status":"APPLIED","workflow_id":"wf","workflow_version":1,"verified":false}`, `{"status":"APPLIED","workflow_id":"wf","workflow_version":2}`, `{"status":"APPLIED","workflow_id":"wf","workflow_version":1,"entity":{"kind":"course","id":"other","name":"其他课"}}`} {
		if _, _, e := s.verifyLearnerResult(context.Background(), "owner", route, model.ControlRun{Output: raw}); e == nil {
			t.Fatal("unverified result accepted", raw)
		}
	}
}
