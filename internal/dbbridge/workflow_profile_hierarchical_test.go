package dbbridge

import (
	f "academic/agent/framework"
	"encoding/json"
	"testing"
)

func TestProfileMetadataHasNoVersionedExecutionPrerequisites(t *testing.T) {
	for _, metadata := range []map[string]any{
		{"version": 1},
		{"version": 2, "retrieval_strategy": "hierarchical"},
		{"version": 3, "retrieval_strategy": map[string]any{"legacy": true}},
	} {
		raw, _ := json.Marshal(map[string]any{"entity": map[string]string{"kind": "course"}, "profile_manifest": metadata})
		if err := validateInitialCourseProfile(f.Workflow{Definition: raw}, []f.Memory{{Category: "notes"}}); err != nil {
			t.Fatal("optional metadata imposed new Workflow fields or knowledge topics", err)
		}
	}
}

func TestProfileSeedContentAndOwnershipValidationRemainIndependent(t *testing.T) {
	seed := profileSeed()
	seed.Category = "course_notes"
	if _, err := normalizeWorkflowProfileSeed(seed); err != nil {
		t.Fatal("valid seed required a fixed profile category", err)
	}
	for _, edit := range []func(*f.Memory){
		func(m *f.Memory) { m.Title = "" },
		func(m *f.Memory) { m.Content = "" },
		func(m *f.Memory) { m.SourceRefs = nil },
		func(m *f.Memory) { m.OwnerID = "other-owner" },
		func(m *f.Memory) { m.WorkflowID = "other-workflow" },
	} {
		bad := seed
		edit(&bad)
		if _, err := normalizeWorkflowProfileSeed(bad); err == nil {
			t.Fatal("invalid atomic seed passed content/ownership checks")
		}
	}
}
