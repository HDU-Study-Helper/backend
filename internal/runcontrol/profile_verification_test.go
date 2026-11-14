package runcontrol

import (
	f "academic/agent/framework"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProfileHandoffOnlyChecksOptionalMetadataObject(t *testing.T) {
	service := &Service{}
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"omitted", `{"entity":{"kind":"course"}}`, true},
		{"null", `{"profile_manifest":null}`, true},
		{"empty", `{"profile_manifest":{}}`, true},
		{"partial", `{"profile_manifest":{"search_coverage":[{"dataset":"schedules","complete":false}],"dimensions":{"identity":{"status":"known","source_refs":[]}}}}`, true},
		{"array", `{"profile_manifest":[]}`, false},
		{"string", `{"profile_manifest":"PRIVATE_SOURCE_FACT"}`, false},
		{"number", `{"profile_manifest":42}`, false},
		{"boolean", `{"profile_manifest":false}`, false},
		{"definition array", `[]`, false},
		{"definition null", `null`, false},
		{"invalid JSON", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := service.verifyWorkflowProfile(context.Background(), "owner", f.Workflow{Definition: json.RawMessage(tc.raw)})
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v want=%v: %v", err == nil, tc.ok, err)
			}
			if err != nil && (f.SafeError(err).Code != "INVALID_WORKFLOW_PROFILE" || strings.Contains(err.Error(), "PRIVATE_SOURCE_FACT")) {
				t.Fatal("unsafe metadata diagnostic", err)
			}
		})
	}
}

func TestProfileHandoffDoesNotRequireKnowledgeCategoriesOrMemoryCount(t *testing.T) {
	// No Agents repository is configured: metadata validation must not invent a
	// profile-index/topic lookup or reject a Workflow for having no such memory.
	service := &Service{}
	for _, raw := range []string{
		`{"profile_manifest":{"version":1}}`,
		`{"profile_manifest":{"version":2,"retrieval_strategy":"custom","query_coverage":[]}}`,
		`{"profile_manifest":{"version":99,"search_coverage":[{"dataset":"schedules","complete":false}],"dimensions":{}}}`,
		`{"profile_manifest":{"dimensions":{"materials":{"status":"partial"}}}}`,
	} {
		if err := service.verifyWorkflowProfile(context.Background(), "owner", f.Workflow{ID: "wf", OwnerID: "owner", Definition: json.RawMessage(raw)}); err != nil {
			t.Fatal("business completeness blocked handoff", err)
		}
	}
}
