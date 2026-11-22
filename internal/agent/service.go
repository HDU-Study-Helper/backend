// Package agent adapts the reusable framework to the backend's existing in-process Bridge.
package agent

import (
	"context"
	"encoding/json"
	"sync"

	f "academic/agent/framework"
	"academic/backend/internal/bridge"
	"academic/backend/internal/chatwire"
	"academic/backend/internal/continuation"
)

const RunOperation = "agent.run"

// Service exposes internal Go and bridge.Port entry points only; it registers no HTTP routes.
type Service struct {
	// ContinuationFinished is an optional host bookkeeping hook. It runs once
	// after restoring the handled chain, before the answer receipt is finalized.
	ContinuationFinished func(context.Context, continuation.ControlLink, []string, f.RunResult) error
	Runtime              *f.Runtime
	Registry             f.Registry
	Tools                *f.ToolRegistry
	backend              bridge.Port
	search               f.SearchProvider
	schedules            bridge.Port
	stageMu              sync.Mutex
	stageActive          map[string]int
	stageTotal           int
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
	s := &Service{Tools: tools, Registry: f.Registry{Repository: d.Repository}, backend: d.Backend, search: d.Search, schedules: d.Schedules}
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
	ctx, err := f.WithExecutionTimeZone(ctx, owner, "Asia/Shanghai")
	if err != nil {
		return f.RunResult{}, err
	}
	a, err := s.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: owner, AgentID: req.AgentID})
	if err != nil {
		return f.RunResult{}, err
	}
	return s.runWithContinuation(ctx, owner, req, a)
}

func (s *Service) runRole(ctx context.Context, owner string, req f.RunRequest, a f.AgentInstance) (f.RunResult, error) {
	if a.Type == "learner" {
		return s.runLearner(ctx, owner, req, a)
	}
	switch a.Type {
	case "chat":
		ctx = f.WithOutputValidation(ctx, f.OutputValidation{OwnerID: owner, AgentID: a.ID, MaxCorrections: 2, Validate: chatwire.ValidateOutput})
	case "worker":
		ctx = f.WithOutputValidation(ctx, f.OutputValidation{OwnerID: owner, AgentID: a.ID, MaxCorrections: 2, Validate: validateWorkerFinal})
	case "trigger", "filter":
		ctx = f.WithOutputValidation(ctx, f.OutputValidation{OwnerID: owner, AgentID: a.ID, MaxCorrections: 2, Validate: validateJSONObjectFinal})
	}
	return s.Runtime.Run(ctx, owner, req)
}

// Role contracts are installed by this application host, never by the generic
// Runtime. This minimum policy checks syntax only; it cannot approve business work.
func validateJSONObjectFinal(ctx context.Context, input f.OutputValidationInput) (*f.OutputValidationFailure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var value map[string]json.RawMessage
	if !json.Valid([]byte(input.Output)) || json.Unmarshal([]byte(input.Output), &value) != nil || value == nil {
		return &f.OutputValidationFailure{Kind: f.OutputFormat, Code: "INVALID_AGENT_RESULT", Feedback: "Return exactly one valid JSON object matching your existing output contract. Correct syntax only; do not change facts, create new actions or repeat tools."}, nil
	}
	return nil, nil
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
		caller, lookupErr := service.Registry.Repository.GetAgent(ctx, f.Scope{OwnerID: e.Scope.OwnerID, AgentID: e.Scope.AgentID})
		if lookupErr != nil {
			return f.RunResult{}, lookupErr
		}
		if target.Type == "worker" && caller.Type != "chat" && caller.Type != "filter" {
			return f.RunResult{}, f.Fail("PERMISSION_DENIED", "only Filter or Chat can dispatch Worker")
		}
		if target.Type == "chat" || caller.Type == "chat" && target.Type != "worker" && target.Type != "learner" {
			return f.RunResult{}, f.Fail("PERMISSION_DENIED", "Chat can dispatch only Worker or Learner and is never a dispatch target")
		}
		if caller.Type == "chat" {
			src, ok := continuation.Current(ctx)
			if !ok || !src.Valid(e.Scope.OwnerID) || src.Channel != continuation.ChB {
				return f.RunResult{}, f.Fail("PERMISSION_DENIED", "Chat dispatch requires an authenticated conversation")
			}
		}

		if target.Type == "learner" {
			switch caller.Type {
			case "filter", "chat":
				if caller.Type == "chat" && e.Scope.WorkflowID != "" {
					ctx = context.WithValue(ctx, learnerStageKey{}, learnerAuthority{Owner: e.Scope.OwnerID, Path: "REVISE", Target: e.Scope.WorkflowID, Role: "chat"})
				}
				if e.Scope.WorkflowID == "" {
					ctx = context.WithValue(ctx, learnerStageKey{}, learnerAuthority{Owner: e.Scope.OwnerID, Path: "INITIALIZE", Role: caller.Type})
				}
			case "worker":
				if e.Scope.WorkflowID == "" {
					return f.RunResult{}, f.Fail("PERMISSION_DENIED", "Worker revision requires its bound Workflow")
				}
				ctx = context.WithValue(ctx, learnerStageKey{}, learnerAuthority{Owner: e.Scope.OwnerID, Path: "REVISE", Target: e.Scope.WorkflowID, Role: "worker"})
			default:
				return f.RunResult{}, f.Fail("PERMISSION_DENIED", "only Filter initialization and bound Worker revision may call Learner")
			}
		}
		if target.Type == "learner" && e.Scope.WorkflowID != "" {
			// The builtin binds the selected workflow before dispatch. Resolve the
			// caller role from its trusted identity, never from model input.
			if caller.Type == "filter" {
				hint := "课程业务执行应派发Worker，请核实target_agent_id并携带workflow:{workflow_id,version}；Learner创建、更新和管理请求不得绑定workflow，请移除顶层workflow，并通过context.target_workflow_id指定待管理的Workflow"
				candidates, listErr := service.Registry.Repository.ListAgents(ctx, e.Scope.OwnerID, 100, "")
				if listErr == nil {
					id := ""
					ambiguous := false
					for _, candidate := range candidates {
						if candidate.Type == "worker" && candidate.Status == "enabled" {
							if id != "" {
								ambiguous = true
							}
							id = candidate.ID
						}
					}
					if id != "" && !ambiguous && len(candidates) < 100 {
						hint += "；本账号已核实Worker的target_agent_id=" + id + "，请使用这个完整ID，不要填worker等角色名，也不要使用Learner的ID。"
					}
				}
				return f.RunResult{}, f.Fail("INVALID_ARGUMENT", hint)
			}
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
