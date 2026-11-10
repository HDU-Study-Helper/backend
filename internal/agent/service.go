// Package agent adapts the reusable framework to the backend's existing in-process Bridge.
package agent

import (
	"context"
	"encoding/json"

	f "academic/agent/framework"
	"academic/backend/internal/bridge"
)

const RunOperation = "agent.run"

// Service exposes internal Go and bridge.Port entry points only; it registers no HTTP routes.
type Service struct {
	Runtime  *f.Runtime
	Registry f.Registry
	Tools    *f.ToolRegistry
}
type Dependencies struct {
	Repository f.Repository
	Providers  map[string]f.ModelProvider
	Search     f.SearchProvider
	Backend    bridge.Port
	Schedules  bridge.Port
}

func New(d Dependencies) (*Service, error) {
	if d.Repository == nil {
		return nil, f.Fail("INVALID_ARGUMENT", "agent repository required")
	}
	tools := f.NewToolRegistry()
	s := &Service{Tools: tools, Registry: f.Registry{Repository: d.Repository}}
	dispatch := DispatchBridge{Port: s}
	backend := BackendBridge{Port: d.Backend}
	if err := (f.Builtins{Repository: d.Repository, Search: d.Search, Dispatcher: dispatch, Notifications: backend, Data: backend}).Register(tools); err != nil {
		return nil, err
	}
	if d.Schedules != nil {
		if err := RegisterScheduleTools(tools, d.Schedules); err != nil {
			return nil, err
		}
	}
	s.Runtime = f.NewRuntime(d.Repository, tools, d.Providers)
	return s, nil
}
func (s *Service) Run(ctx context.Context, owner string, req f.RunRequest) (f.RunResult, error) {
	return s.Runtime.Run(ctx, owner, req)
}
func (s *Service) Call(ctx context.Context, req bridge.Request) (bridge.Reply, error) {
	if req.Version != bridge.Version || req.OwnerID == "" || len(req.Payload) > 1<<20 || !json.Valid(req.Payload) {
		return bridge.Reply{}, f.Fail("INVALID_ARGUMENT", "invalid bridge request")
	}
	if req.Operation != RunOperation {
		return bridge.Reply{}, bridge.ErrUnsupported
	}
	var run f.RunRequest
	if err := json.Unmarshal(req.Payload, &run); err != nil {
		return bridge.Reply{}, f.Fail("INVALID_ARGUMENT", "invalid run payload")
	}
	result, err := s.Run(ctx, req.OwnerID, run)
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return bridge.Reply{}, marshalErr
	}
	return bridge.Reply{Version: bridge.Version, Operation: req.Operation, Payload: raw}, err
}

type DispatchBridge struct{ Port bridge.Port }

func (b DispatchBridge) Dispatch(ctx context.Context, e f.ExecutionContext, req f.RunRequest) (f.RunResult, error) {
	if !e.Has("agent.dispatch") {
		return f.RunResult{}, f.Fail("PERMISSION_DENIED", "dispatch permission required")
	}
	if b.Port == nil {
		return f.RunResult{}, f.Fail("NOT_IMPLEMENTED", "dispatch bridge unavailable")
	}
	if service, ok := b.Port.(*Service); ok {
		target, lookupErr := service.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: e.Scope.OwnerID, AgentID: req.AgentID})
		if lookupErr != nil {
			return f.RunResult{}, lookupErr
		}
		if target.Type == "worker" && e.Scope.WorkflowID == "" {
			return f.RunResult{}, f.Fail("INVALID_ARGUMENT", "Worker业务调用必须在agent_dispatch顶层提供workflow:{workflow_id,version}；context中的Workflow文本不能绑定执行范围，请使用已核实的ID与整数版本重新派发")
		}
	}
	req.Trigger = f.AgentDispatch
	raw, err := json.Marshal(req)
	if err != nil {
		return f.RunResult{}, f.Fail("INVALID_ARGUMENT", "invalid dispatch request")
	}
	reply, err := b.Port.Call(f.DispatchContext(ctx, e), bridge.Request{Version: bridge.Version, OwnerID: e.Scope.OwnerID, Operation: RunOperation, Payload: raw})
	if err != nil {
		return f.RunResult{}, err
	}
	if reply.Version != bridge.Version || reply.Operation != RunOperation {
		return f.RunResult{}, f.Fail("INTERNAL_ERROR", "invalid dispatch reply")
	}
	var result f.RunResult
	if json.Unmarshal(reply.Payload, &result) != nil {
		return result, f.Fail("INTERNAL_ERROR", "invalid dispatch result")
	}
	return result, nil
}

// BackendBridge converts trusted owner-scoped framework requests to existing Bridge envelopes.
type BackendBridge struct{ Port bridge.Port }

func (b BackendBridge) call(ctx context.Context, scope f.Scope, operation string, input, output any) error {
	if b.Port == nil {
		return f.Fail("NOT_IMPLEMENTED", "backend service unavailable")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return f.Fail("INVALID_ARGUMENT", "invalid backend request")
	}
	reply, err := b.Port.Call(ctx, bridge.Request{Version: bridge.Version, OwnerID: scope.OwnerID, Operation: operation, Payload: raw})
	if err != nil {
		return err
	}
	if reply.Version != bridge.Version || reply.Operation != operation || json.Unmarshal(reply.Payload, output) != nil {
		return f.Fail("INTERNAL_ERROR", "invalid backend service reply")
	}
	return nil
}
func (b BackendBridge) Send(ctx context.Context, s f.Scope, n f.Notification) (receipt f.NotificationReceipt, err error) {
	err = b.call(ctx, s, "notification.send", n, &receipt)
	return
}
func (b BackendBridge) Catalog(ctx context.Context, s f.Scope) (catalog []f.Dataset, err error) {
	err = b.call(ctx, s, "data.catalog", map[string]any{}, &catalog)
	return
}
func (b BackendBridge) Query(ctx context.Context, s f.Scope, q f.Query) (result f.QueryResult, err error) {
	err = b.call(ctx, s, "data.query", q, &result)
	return
}

func (b BackendBridge) SearchKeywords(ctx context.Context, s f.Scope, q f.KeywordSearchRequest) (result f.KeywordSearchResult, err error) {
	err = b.call(ctx, s, "data.search", q, &result)
	return
}

// BackendServices is a host-side bridge adapter. Notifications remain owned by the backend.
// A missing notification implementation fails explicitly; tests can inject a mock.
type BackendServices struct {
	Notifications f.NotificationService
	Data          f.DataService
}

func (b BackendServices) Call(ctx context.Context, req bridge.Request) (bridge.Reply, error) {
	if req.Version != bridge.Version || req.OwnerID == "" || len(req.Payload) > 1<<20 || !json.Valid(req.Payload) {
		return bridge.Reply{}, f.Fail("INVALID_ARGUMENT", "invalid service request")
	}
	scope := f.Scope{OwnerID: req.OwnerID}
	var result any
	var err error
	switch req.Operation {
	case "notification.send":
		if b.Notifications == nil {
			return bridge.Reply{}, f.Fail("NOT_IMPLEMENTED", "notification service unavailable")
		}
		var n f.Notification
		if json.Unmarshal(req.Payload, &n) != nil {
			return bridge.Reply{}, f.Fail("INVALID_ARGUMENT", "invalid notification")
		}
		result, err = b.Notifications.Send(ctx, scope, n)
	case "data.catalog":
		if b.Data == nil {
			return bridge.Reply{}, f.Fail("NOT_IMPLEMENTED", "data service unavailable")
		}
		result, err = b.Data.Catalog(ctx, scope)
	case "data.query":
		if b.Data == nil {
			return bridge.Reply{}, f.Fail("NOT_IMPLEMENTED", "data service unavailable")
		}
		var q f.Query
		if json.Unmarshal(req.Payload, &q) != nil {
			return bridge.Reply{}, f.Fail("INVALID_ARGUMENT", "invalid query")
		}
		result, err = b.Data.Query(ctx, scope, q)
	case "data.search":
		search, ok := b.Data.(f.KeywordSearchService)
		if !ok {
			return bridge.Reply{}, f.Fail("NOT_IMPLEMENTED", "keyword search unavailable")
		}
		var q f.KeywordSearchRequest
		if json.Unmarshal(req.Payload, &q) != nil {
			return bridge.Reply{}, f.Fail("INVALID_ARGUMENT", "invalid keyword search")
		}
		result, err = search.SearchKeywords(ctx, scope, q)
	default:
		return bridge.Reply{}, bridge.ErrUnsupported
	}
	if err != nil {
		return bridge.Reply{}, err
	}
	raw, err := json.Marshal(result)
	return bridge.Reply{Version: bridge.Version, Operation: req.Operation, Payload: raw}, err
}

var _ bridge.Port = (*Service)(nil)
var _ f.Dispatcher = DispatchBridge{}
