package agent

import (
	"context"
	"encoding/json"
	"testing"

	f "academic/agent/framework"
	stage "academic/backend/internal/learnerstage"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type stageSearchStub struct{ calls int }

func (p *stageSearchStub) Search(context.Context, f.SearchRequest) ([]f.SearchResult, error) {
	p.calls++
	return []f.SearchResult{}, nil
}
func (p *stageSearchStub) Fetch(context.Context, string) (f.Page, error) {
	p.calls++
	return f.Page{}, nil
}

func TestStagingStateContractAndRegistrationTransform(t *testing.T) {
	var schema any
	_ = json.Unmarshal(learnerStageStateSchema(), &schema)
	c := jsonschema.NewCompiler()
	if err := c.AddResource("urn:state", schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile("urn:state")
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{"protocol": "learner_staging_v2", "session_id": "session", "path": "INITIALIZE", "workflow_id": "", "workflow_status": "", "memory_count": 0, "base_version": nil, "staging_revision": 0, "staged": true, "committed": false, "completion_blockers": []any{}}
	if err := compiled.Validate(good); err != nil {
		t.Fatal(err)
	}
	delete(good, "session_id")
	if compiled.Validate(good) == nil {
		t.Fatal("missing trusted identity accepted")
	}
	r := f.NewToolRegistry()
	if err := (f.Builtins{TransformSpec: stagingToolSpec}).Register(r); err != nil {
		t.Fatal(err)
	}
	for _, spec := range r.Catalog() {
		if spec.Name == "workflow_create" {
			var in map[string]any
			_ = json.Unmarshal(spec.InputSchema, &in)
			if _, ok := in["properties"].(map[string]any)["initial_private_memories"]; ok {
				t.Fatal("unsupported seed option remains executable")
			}
		}
	}
}

func TestLearnerStagingWorkflowUpdatePreservesOmittedMetadata(t *testing.T) {
	repo := stagingFixture()
	entity := json.RawMessage(`{"kind":"course","id":"specific-course","name":"Calculus"}`)
	repo.snapshot.Workflow = f.Workflow{ID: "target", OwnerID: "owner", Name: "Original", Description: "Keep description", ParentID: "parent", Status: "active", Version: 4, Definition: mustStageJSON(map[string]any{"entity": entity})}
	session := stage.New(repo, repo, "owner", "learner", "REVISE", "target")
	if err := session.Select(context.Background(), "target"); err != nil {
		t.Fatal(err)
	}
	round := 0
	runtime := f.NewRuntime(session, f.NewToolRegistry(), map[string]f.ModelProvider{"learner": fnModel(func(_ context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		round++
		if round == 1 {
			return stagedCalls("workflow_update", `{"workflow_id":"target","version":4,"name":"Updated","definition":{"entity":{"kind":"course","id":"specific-course","name":"Calculus"},"goal":"new goal"}}`), nil
		}
		for _, m := range q.Messages {
			if m.Role == "tool" {
				var result f.ToolResult
				_ = json.Unmarshal([]byte(m.Content), &result)
				if !result.OK {
					t.Fatal(result)
				}
			}
		}
		return done(), nil
	})})
	if err := (f.Builtins{Repository: session, TransformSpec: stagingToolSpec}).Register(runtime.Tools); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.Run(context.Background(), "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	w := session.View.Workflow
	if w.Status != "active" || w.Description != "Keep description" || w.ParentID != "parent" || w.Name != "Updated" || repo.commits != 0 {
		t.Fatal(w, repo.commits)
	}
}

func TestLearnerStagingReadDependenciesAndWriteIsolation(t *testing.T) {
	repo := stagingFixture()
	a := repo.agents["learner"]
	a.EnabledTools = append(a.EnabledTools, "schedule_query", "schedule_get", "schedule_create", "schedule_update", "schedule_delete", "web_search")
	a.Permissions = append(a.Permissions, "schedule.read", "schedule.write", "schedule.delete", "web.search")
	repo.agents[a.ID] = a
	p := &scheduleContractPort{}
	search := &stageSearchStub{}
	round := 0
	var first []f.ToolSpec
	svc, err := New(Dependencies{Repository: repo, Schedules: p, Search: search, Providers: map[string]f.ModelProvider{"learner": fnModel(func(_ context.Context, q f.ModelRequest) (f.ModelResponse, error) {
		round++
		if round == 1 {
			first = q.Tools
			return stagedCalls("schedule_query", `{"from":"2026-11-01T00:00:00+08:00","to":"2026-12-01T00:00:00+08:00"}`), nil
		}
		if round == 2 {
			return stagedCalls("web_search", `{"query":"fixture","count":1}`), nil
		}
		return done(), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = svc.Run(WithLearnerStage(context.Background(), "owner", "INITIALIZE", ""), "owner", f.RunRequest{AgentID: "learner", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
	names := map[string]f.ToolSpec{}
	for _, s := range first {
		names[s.Name] = s
	}
	if _, ok := names["schedule_query"]; !ok {
		t.Fatal("configured schedule query missing")
	}
	if _, ok := names["web_search"]; !ok {
		t.Fatal("configured web search missing")
	}
	for _, name := range []string{"schedule_create", "schedule_update", "schedule_delete"} {
		if _, ok := names[name]; ok {
			t.Fatal("staging exposed a write", name)
		}
	}
	if !names["learner_stage_state"].ReadOnly || p.calls != 1 || search.calls != 1 || repo.commits != 0 {
		t.Fatal(names["learner_stage_state"], p.calls, search.calls, repo.commits)
	}
}
