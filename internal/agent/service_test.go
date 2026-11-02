package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/bridge"
)

type repoStub struct {
	f.Repository
	mu     sync.Mutex
	agents map[string]f.AgentInstance
	runs   map[string]f.RunRecord
}

func (r *repoStub) GetAgent(_ context.Context, s f.Scope) (f.AgentInstance, error) {
	a, ok := r.agents[s.AgentID]
	if !ok || a.OwnerID != s.OwnerID {
		return a, f.Fail("AGENT_NOT_FOUND", "not owned")
	}
	return a, nil
}
func (*repoStub) ListMemory(context.Context, f.Scope, string, int) ([]f.Memory, error) {
	return []f.Memory{}, nil
}
func (r *repoStub) SaveRun(_ context.Context, run f.RunRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[run.ID] = run
	return nil
}

type fnModel func(context.Context, f.ModelRequest) (f.ModelResponse, error)

func (fn fnModel) Complete(ctx context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	return fn(ctx, r)
}
func testAgent(id, owner string) f.AgentInstance {
	return f.AgentInstance{ID: id, OwnerID: owner, Status: "enabled", AgentDefinition: f.AgentDefinition{Name: id, SystemPrompt: "test", Model: f.ModelConfig{Provider: id, Model: id}, EnabledTools: []string{"agent_dispatch"}, Permissions: []string{"agent.dispatch"}, Limits: f.Limits{}.Defaults()}}
}
func toolCall(target string) f.ModelResponse {
	args, _ := json.Marshal(map[string]string{"target_agent_id": target, "task": "test"})
	return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "dispatch", Type: "function", Function: f.FunctionCall{Name: "agent_dispatch", Arguments: string(args)}}}}, FinishReason: "tool_calls"}
}
func done() f.ModelResponse {
	return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}
}
func errCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || f.SafeError(err).Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}
func TestBridgeDispatchAuthorizationAndTermination(t *testing.T) {
	for _, tc := range []struct {
		name                                                    string
		otherOwner, disabled, cycle, noPermission, sharedBudget bool
		code                                                    string
	}{
		{name: "other owner", otherOwner: true, code: "AGENT_NOT_FOUND"},
		{name: "disabled target", disabled: true, code: "PERMISSION_DENIED"},
		{name: "cycle", cycle: true, code: "EXECUTION_LIMIT_EXCEEDED"},
		{name: "missing dispatch permission", noPermission: true, code: "PERMISSION_DENIED"},
		{name: "shared model budget", sharedBudget: true, code: "EXECUTION_LIMIT_EXCEEDED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := testAgent("a", "owner"), testAgent("b", "owner")
			if tc.otherOwner {
				b.OwnerID = "other"
			}
			if tc.disabled {
				b.Status = "disabled"
			}
			if tc.noPermission {
				a.Permissions = nil
			}
			if tc.sharedBudget {
				a.Limits.ModelCalls = 1
			}
			repo := &repoStub{agents: map[string]f.AgentInstance{"a": a, "b": b}, runs: map[string]f.RunRecord{}}
			models := map[string]f.ModelProvider{}
			models["a"] = fnModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
				if r.Messages[len(r.Messages)-1].Role == "tool" {
					if !strings.Contains(r.Messages[len(r.Messages)-1].Content, tc.code) {
						t.Fatal("missing dispatch rejection", r.Messages[len(r.Messages)-1].Content)
					}
					return done(), nil
				}
				return toolCall("b"), nil
			})
			models["b"] = fnModel(func(_ context.Context, r f.ModelRequest) (f.ModelResponse, error) {
				if !tc.cycle {
					t.Fatal("forbidden child model executed")
				}
				return toolCall("a"), nil
			})
			s, err := New(Dependencies{Repository: repo, Providers: models})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Run(context.Background(), "owner", f.RunRequest{AgentID: "a", Trigger: f.UserMessage, Input: json.RawMessage(`{}`)})
			if tc.cycle || tc.sharedBudget {
				errCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			if len(repo.runs) > 2 {
				t.Fatal("recursion was not bounded")
			}
		})
	}
}
func TestBridgeProtocolValidation(t *testing.T) {
	s, err := New(Dependencies{Repository: &repoStub{agents: map[string]f.AgentInstance{}, runs: map[string]f.RunRecord{}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []bridge.Request{{Version: "old", OwnerID: "u", Operation: RunOperation, Payload: json.RawMessage(`{}`)}, {Version: bridge.Version, Operation: RunOperation, Payload: json.RawMessage(`{}`)}, {Version: bridge.Version, OwnerID: "u", Operation: RunOperation, Payload: json.RawMessage(`invalid`)}} {
		_, err := s.Call(context.Background(), req)
		errCode(t, err, "INVALID_ARGUMENT")
	}
	_, err = s.Call(context.Background(), bridge.Request{Version: bridge.Version, OwnerID: "u", Operation: "unknown", Payload: json.RawMessage(`{}`)})
	if err != bridge.ErrUnsupported {
		t.Fatal(err)
	}
	_, err = (BackendBridge{}).Catalog(context.Background(), f.Scope{OwnerID: "u"})
	errCode(t, err, "NOT_IMPLEMENTED")
}
