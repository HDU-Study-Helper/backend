package runcontrol

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"context"
	"encoding/json"
	"testing"
)

type profileVerifyRepo struct {
	f.Repository
	memories []f.Memory
	seen     f.Scope
}

func (r *profileVerifyRepo) ListMemory(_ context.Context, s f.Scope, _ string, _ int) ([]f.Memory, error) {
	r.seen = s
	return r.memories, nil
}
func TestProfileVerificationRequiresBoundPrivateKnowledge(t *testing.T) {
	p := map[string]any{"version": 1, "keywords": []string{"数学分析", "数分"}, "search_coverage": []any{map[string]any{"dataset": "schedules", "complete": true}}, "dimensions": map[string]any{}}
	p["search_coverage"] = []any{}
	for _, dataset := range []string{"platform_records", "academic_offerings", "academic_items", "schedules", "dws_messages", "dws_attachments"} {
		p["search_coverage"] = append(p["search_coverage"].([]any), map[string]any{"dataset": dataset, "complete": true})
	}
	for _, key := range []string{"identity", "timetable", "teaching_progress", "assignments", "materials", "discussion_participation"} {
		p["dimensions"].(map[string]any)[key] = map[string]any{"status": "partial", "source_refs": []string{"platform_records:1"}}
	}
	raw, _ := json.Marshal(map[string]any{"profile_manifest": p})
	w := f.Workflow{ID: "wf", OwnerID: "owner", CreatedBy: "learner", Definition: raw}
	refs := json.RawMessage(`["platform_records:1"]`)
	r := &profileVerifyRepo{memories: []f.Memory{{Visibility: "PRI", WorkflowID: "wf", Status: "active", Category: "profile_index", SourceRefs: refs}, {Visibility: "PRI", WorkflowID: "wf", Status: "active", Category: "identity", SourceRefs: refs}}}
	s := &Service{Agents: &agent.Service{Registry: f.Registry{Repository: r}}}
	if err := s.verifyWorkflowProfile(context.Background(), "owner", w); err != nil {
		t.Fatal(err)
	}
	if r.seen.OwnerID != "owner" || r.seen.WorkflowID != "wf" || r.seen.AgentID != "learner" {
		t.Fatal("untrusted scope", r.seen)
	}
	r.memories[1].WorkflowID = "other"
	if s.verifyWorkflowProfile(context.Background(), "owner", w) == nil {
		t.Fatal("foreign memory accepted")
	}
	r.memories[1].WorkflowID = "wf"
	r.memories[0].AgentID = "learner"
	if s.verifyWorkflowProfile(context.Background(), "owner", w) == nil {
		t.Fatal("Agent PRI used as Workflow profile")
	}
	w.Definition = json.RawMessage(`{"profile_manifest":{"version":1,"keywords":["数分"]}}`)
	if s.verifyWorkflowProfile(context.Background(), "owner", w) == nil {
		t.Fatal("missing coverage accepted")
	}
}
