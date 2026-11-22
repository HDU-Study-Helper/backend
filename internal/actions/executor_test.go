package actions

import (
	f "academic/agent/framework"
	"academic/backend/internal/model"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type repoFake struct {
	model.ActionRepository
	j           model.ClaimedAction
	status, ref string
	reads       int
}

func (r *repoFake) ClaimAction(context.Context) (model.ClaimedAction, error) { return r.j, nil }
func (r *repoFake) FinishAction(_ context.Context, _ model.ClaimedAction, s, ref string) error {
	r.status = s
	r.ref = ref
	return nil
}
func (r *repoFake) ListActions(_ context.Context, owner, kind string, limit, offset int) ([]model.PublicAction, error) {
	if owner != r.j.OwnerID {
		panic("scope leak")
	}
	r.reads++
	return []model.PublicAction{}, nil
}

type agentFake struct{ f.AgentInstance }

func (a agentFake) GetAgent(context.Context, f.Scope) (f.AgentInstance, error) {
	return a.AgentInstance, nil
}
func TestExecutionChecksCurrentGrantsAndOwner(t *testing.T) {
	due := time.Now()
	r := &repoFake{j: model.ClaimedAction{OwnerID: "u", PublicAction: model.PublicAction{ID: "j", AgentID: "a", Input: model.ActionInput{Key: "k", Evidence: []string{"message:42"}, Due: &due, Kind: "ToolCall", Tool: "schedule_get", Args: json.RawMessage(`{"id":"s"}`)}}}}
	a := f.AgentInstance{ID: "a", OwnerID: "u", Status: "enabled", AgentDefinition: f.AgentDefinition{Type: "worker", Permissions: []string{"job.write", "schedule.read"}, EnabledTools: []string{"planned_job_create", "schedule_get"}}}
	calls := 0
	x := Executor{Repo: r, Agents: agentFake{a}, Tools: func(_ context.Context, j model.ClaimedAction) (string, error) { calls++; return "schedule:s", nil }}
	if ok, e := x.RunOne(context.Background()); !ok || e != nil || r.status != "succeeded" || r.ref != "schedule:s" {
		t.Fatal(ok, e, r.status)
	}
	for _, change := range []func(*f.AgentInstance){func(a *f.AgentInstance) { a.OwnerID = "other" }, func(a *f.AgentInstance) { a.Status = "disabled" }, func(a *f.AgentInstance) { a.Permissions = []string{"job.write"} }, func(a *f.AgentInstance) { a.EnabledTools = []string{"schedule_get"} }} {
		b := a
		change(&b)
		x.Agents = agentFake{b}
		if _, e := x.RunOne(context.Background()); e == nil || r.status != "failed" || calls != 1 {
			t.Fatal("executed after revoke", e)
		}
	}
	x.Agents = agentFake{a}
	r.j.Input.Args = json.RawMessage(`{"owner_id":"foreign","id":"s"}`)
	if _, e := x.RunOne(context.Background()); e == nil || calls != 1 {
		t.Fatal("target injection")
	}
	r.j.Input.Tool = "bridge_call"
	if _, e := x.RunOne(context.Background()); e == nil || calls != 1 {
		t.Fatal("arbitrary bridge execution")
	}
	r.j.Input.Tool = ""
	r.j.Input.Args = nil
	r.j.Input.Kind = "Worker"
	r.j.Input.Body = "Review latest evidence"
	x.Worker = func(_ context.Context, j model.ClaimedAction) (string, error) {
		if j.AgentID != "a" {
			panic("not self")
		}
		return "actual-run", nil
	}
	if _, e := x.RunOne(context.Background()); e != nil || r.ref != "run:actual-run" {
		t.Fatal(e, r.ref)
	}
	a.Type = "chat"
	x.Agents = agentFake{a}
	if _, e := x.RunOne(context.Background()); !errors.Is(e, model.ErrUnsupported) {
		t.Fatal("nonworker", e)
	}
}
func TestActionContracts(t *testing.T) {
	reg := f.NewToolRegistry()
	if err := RegisterTools(reg, &repoFake{}); err != nil {
		t.Fatal(err)
	}
	if len(reg.Catalog()) != 8 {
		t.Fatal(len(reg.Catalog()))
	}
	for _, s := range reg.Catalog() {
		var schema struct {
			Additional bool           `json:"additionalProperties"`
			Properties map[string]any `json:"properties"`
		}
		if json.Unmarshal(s.InputSchema, &schema) != nil || schema.Additional || schema.Properties["owner_id"] != nil || schema.Properties["agent_id"] != nil {
			t.Fatal("unscoped tool", s.Name)
		}
	}
}
