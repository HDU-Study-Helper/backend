package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"testing"
)

func TestProfileMetadataDoesNotAddVersionedDefinitionRequirements(t *testing.T) {
	service := &Service{}
	for _, raw := range []string{
		`{"entity":{"kind":"course","id":"course"},"profile_manifest":{"version":1,"keywords":[]}}`,
		`{"entity":{"kind":"course","id":"course"},"profile_manifest":{"version":2,"retrieval_strategy":"hierarchical"}}`,
		`{"entity":{"kind":"course","id":"course"},"profile_manifest":{"version":"legacy","query_coverage":"historical metadata","dimensions":["open metadata"]}}`,
	} {
		if err := service.verifyWorkflowProfile(context.Background(), "owner", f.Workflow{Definition: json.RawMessage(raw)}); err != nil {
			t.Fatal("metadata imposed mandatory execution fields or search coverage", err)
		}
	}
}

func TestLearnerHandoffStillRequiresActualPersistedWorkflowIdentity(t *testing.T) {
	valid := f.Workflow{ID: "wf", OwnerID: "owner", Version: 1, Status: "active", Definition: json.RawMessage(`{"entity":{"kind":"course","id":"course/math-101","name":"Mathematics 101"},"profile_manifest":{"search_coverage":[{"complete":false}]}}`)}
	for _, tc := range []struct {
		name string
		edit func(*f.Workflow)
	}{
		{"wrong owner", func(w *f.Workflow) { w.OwnerID = "foreign" }},
		{"inactive", func(w *f.Workflow) { w.Status = "archived" }},
		{"wrong version", func(w *f.Workflow) { w.Version = 2 }},
		{"invalid entity", func(w *f.Workflow) { w.Definition = json.RawMessage(`{"profile_manifest":{}}`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := valid
			tc.edit(&w)
			service := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: learnerVerificationRepo{w: w}}}}
			_, _, err := service.verifyLearnerResult(context.Background(), "owner", itemRoute{}, model.ControlRun{Output: `{"status":"APPLIED","workflow_id":"wf","workflow_version":1,"verified":true}`})
			if err == nil {
				t.Fatal("removing completeness gates removed persisted Workflow checks")
			}
		})
	}
	service := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: learnerVerificationRepo{w: valid}}}}
	if _, _, err := service.verifyLearnerResult(context.Background(), "owner", itemRoute{}, model.ControlRun{Output: `{"status":"APPLIED","workflow_id":"wf","workflow_version":1,"verified":true}`}); err != nil {
		t.Fatal("partial metadata blocked actual persisted Workflow", err)
	}
}
