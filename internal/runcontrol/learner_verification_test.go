package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"strings"
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

func TestHostLearnerVerificationResultsSelectionAndLegacyGates(t *testing.T) {
	valid := `{"status":"APPLIED","workflow_id":"wf","workflow_version":1,"verified":true}`
	for _, tc := range []struct {
		name, output, route, workflowChange, wantError string
	}{
		{name: "nested applied", output: `{"results":[` + valid + `]}`, route: "wf"},
		{name: "nested missing flag", output: `{"results":[{"status":"APPLIED","workflow_id":"wf","workflow_version":1}]}`},
		{name: "nested no change false", output: `{"results":[{"status":"NO_CHANGE","workflow_id":"wf","workflow_version":1,"verified":false}]}`, route: "wf"},
		{name: "nested applied false", output: `{"results":[{"status":"APPLIED","workflow_id":"wf","workflow_version":1,"verified":false}]}`, wantError: "未返回已核验"},
		{name: "select exact id", output: `{"results":[{"status":"FAILED","workflow_id":"other"},` + valid + `]}`, route: "wf"},
		{name: "missing route id", output: `{"results":[` + valid + `]}`, route: "other", wantError: "不匹配"},
		{name: "ambiguous without route", output: `{"results":[` + valid + `,{"status":"FAILED","workflow_id":"other"}]}`, wantError: "歧义"},
		{name: "duplicate valid id", output: `{"results":[` + valid + `,` + valid + `]}`, route: "wf", wantError: "歧义"},
		{name: "duplicate failed id", output: `{"results":[` + valid + `,{"status":"FAILED","workflow_id":"wf"}]}`, route: "wf", wantError: "歧义"},
		{name: "empty results", output: `{"results":[]}`, wantError: "非空"},
		{name: "null results", output: `{"results":null}`, wantError: "非空"},
		{name: "null entry", output: `{"results":[null]}`, wantError: "无效结果"},
		{name: "garbage entry", output: `{"results":[42]}`, wantError: "非空"},
		{name: "missing id entry", output: `{"results":[{}]}`, wantError: "无效结果"},
		{name: "invalid json", output: `garbage`, wantError: "未返回已核验"},
		{name: "unknown workflow", output: `{"results":[{"status":"APPLIED","workflow_id":"unknown","workflow_version":1}]}`, wantError: "not owned"},
		{name: "wrong version", output: `{"results":[{"status":"APPLIED","workflow_id":"wf","workflow_version":2}]}`, wantError: "版本"},
		{name: "wrong owner", workflowChange: "owner", wantError: "not owned"},
		{name: "inactive", workflowChange: "inactive", wantError: "状态"},
		{name: "invalid entity", workflowChange: "entity", wantError: "实体"},
		{name: "entity mismatch", output: `{"results":[{"status":"APPLIED","workflow_id":"wf","workflow_version":1,"entity":{"kind":"course","id":"other","name":"Other"}}]}`, wantError: "不一致"},
		{name: "optional profile metadata", workflowChange: "profile"},
		{name: "nonobject profile metadata", workflowChange: "profile-shape", wantError: "profile_manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.Workflow{ID: "wf", OwnerID: "owner", Version: 1, Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"course/class","name":"数学"}}`)}
			switch tc.workflowChange {
			case "owner":
				w.OwnerID = "other"
			case "inactive":
				w.Status = "disabled"
			case "entity":
				w.Definition = json.RawMessage(`{}`)
			case "profile":
				w.Definition = json.RawMessage(`{"entity":{"kind":"course","id":"course/class","name":"数学"},"profile_manifest":{"version":2}}`)
			case "profile-shape":
				w.Definition = json.RawMessage(`{"entity":{"kind":"course","id":"course/class","name":"数学"},"profile_manifest":[]}`)
			}
			repo := &itemsIntegrationRepo{workflows: map[string]f.Workflow{"wf": w}}
			s := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: repo}}}
			output := tc.output
			if output == "" {
				output = `{"results":[` + valid + `]}`
			}
			got, _, err := s.verifyLearnerResult(context.Background(), "owner", itemRoute{WorkflowID: tc.route}, model.ControlRun{Output: output})
			if tc.wantError == "" {
				if err != nil || got.ID != "wf" {
					t.Fatalf("got=%+v err=%v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wanted %q, got %v", tc.wantError, err)
			}
		})
	}
}

func TestHostLearnerVerificationSelectsDifferentWorkflowsIndependently(t *testing.T) {
	repo := &itemsIntegrationRepo{workflows: map[string]f.Workflow{}}
	for _, id := range []string{"one", "two"} {
		repo.workflows[id] = f.Workflow{ID: id, OwnerID: "owner", Version: 1, Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"course/class","name":"数学"}}`)}
	}
	s := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: repo}}}
	run := model.ControlRun{Output: `{"results":[{"status":"APPLIED","workflow_id":"one","workflow_version":1,"verified":true},{"status":"NO_CHANGE","workflow_id":"two","workflow_version":1,"verified":false}]}`}
	for _, id := range []string{"one", "two"} {
		got, verified, err := s.verifyLearnerResult(context.Background(), "owner", itemRoute{WorkflowID: id}, run)
		if err != nil || got.ID != id || verified == nil || *verified != (id == "one") {
			t.Fatalf("route=%s got=%+v verified=%v err=%v", id, got, verified, err)
		}
	}
}
