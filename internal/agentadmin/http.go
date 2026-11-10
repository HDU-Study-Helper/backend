package agentadmin

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/bridge"
	"academic/backend/internal/credential"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5"
)

//go:embed agents.html
var page embed.FS

type Console struct {
	Service   *agent.Service
	Settings  *SettingsManager
	repo      f.Repository
	adminRepo model.AgentAdminRepository
	users     model.UserRepository
}

func New(ctx context.Context, repo f.Repository, adminRepo model.AgentAdminRepository, users model.UserRepository, vault *credential.Vault, demo bool, data f.DataService, schedules ...bridge.Port) (*Console, error) {
	settings, err := NewSettings(ctx, adminRepo, vault, demo)
	if err != nil {
		return nil, err
	}
	var schedulePort bridge.Port
	if len(schedules) > 0 {
		schedulePort = schedules[0]
	}
	service, err := agent.New(agent.Dependencies{Repository: repo, Providers: map[string]f.ModelProvider{}, Search: settings, Backend: agent.BackendServices{Data: data}, Schedules: schedulePort})
	if err != nil {
		return nil, err
	}
	service.Runtime.Resolver = settings
	return &Console{Service: service, Settings: settings, repo: repo, adminRepo: adminRepo, users: users}, nil
}
func (*Console) Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	raw, _ := page.ReadFile("agents.html")
	_, _ = w.Write(raw)
}
func (c *Console) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, ok := httpkit.CurrentUser(r.Context())
			if !ok {
				httpkit.Error(w, r, 401, "unauthenticated", "请先登录")
				return
			}
			if actor.Role != model.RoleAdmin {
				httpkit.Error(w, r, 403, "forbidden", "Agent 后台仅管理员可访问")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/settings", func(w http.ResponseWriter, r *http.Request) { httpkit.WriteJSON(w, 200, c.Settings.View()) })
	r.Put("/settings", func(w http.ResponseWriter, r *http.Request) {
		var input Settings
		if httpkit.DecodeJSON(w, r, &input) != nil {
			return
		}
		view, err := c.Settings.Save(r.Context(), input)
		if err != nil {
			respondError(w, r, err)
			return
		}
		httpkit.WriteJSON(w, 200, view)
	})
	r.Get("/tools", func(w http.ResponseWriter, r *http.Request) {
		httpkit.WriteJSON(w, 200, map[string]any{"items": c.Service.Tools.Catalog()})
	})
	r.Get("/workflows", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := c.owner(w, r)
		if !ok {
			return
		}
		items, err := c.repo.ListWorkflows(r.Context(), owner, 100, r.URL.Query().Get("after"))
		if err != nil {
			respondError(w, r, err)
			return
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": items})
	})
	r.Get("/instances/{agent}/memories/{memory}/relations", c.memoryRelations)
	r.Post("/instances/{agent}/memories/{memory}/relations", c.memoryRelations)
	r.Get("/instances", c.listAgents)
	r.Post("/instances", c.saveAgent)
	r.Put("/instances/{agent}", c.saveAgent)
	r.Get("/instances/{agent}/memories", c.listMemories)
	r.Post("/instances/{agent}/memories", c.saveMemory)
	r.Put("/instances/{agent}/memories/{memory}", c.saveMemory)
	r.Delete("/instances/{agent}/memories/{memory}", c.deleteMemory)
	r.Post("/instances/{agent}/runs", c.run)
	r.Get("/instances/{agent}/runs", c.runs)
	return r
}
func respondError(w http.ResponseWriter, r *http.Request, err error) {
	respondRunError(w, r, err, "")
}
func respondRunError(w http.ResponseWriter, r *http.Request, err error, runID string) {
	status := 500
	e := f.SafeError(err)
	switch {
	case errors.Is(err, model.ErrNotFound):
		status = 404
		e = f.Fail("RESOURCE_NOT_FOUND", "记录不存在")
	case errors.Is(err, model.ErrConflict):
		status = 409
		e = f.Fail("CONFIG_CONFLICT", "记录已更新，请刷新后重试")
	case errors.Is(err, model.ErrInvalidArgument):
		status = 400
		e = f.Fail("INVALID_ARGUMENT", "参数无效")
	default:
		switch e.Code {
		case "INVALID_ARGUMENT":
			status = 400
		case "PERMISSION_DENIED":
			status = 403
		case "RESOURCE_NOT_FOUND", "AGENT_NOT_FOUND":
			status = 404
		case "CONFIG_CONFLICT":
			status = 409
		case "RATE_LIMITED":
			status = 429
		case "CANCELLED":
			status = 408
		case "TIMEOUT":
			status = 504
		case "PROVIDER_ERROR":
			status = 502
		case "NOT_IMPLEMENTED":
			status = 503
		case "EXECUTION_LIMIT_EXCEEDED":
			status = 422
		}
	}
	httpkit.WriteJSON(w, status, map[string]any{"error": e, "run_id": runID})
}
func (c *Console) owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	actor, _ := httpkit.CurrentUser(r.Context())
	owner := r.URL.Query().Get("owner_id")
	if owner == "" {
		owner = actor.ID
	}
	u, err := c.users.GetByID(r.Context(), owner)
	if err != nil {
		respondError(w, r, err)
		return "", false
	}
	if u.Disabled {
		httpkit.Error(w, r, 403, "PERMISSION_DENIED", "此用户已停用")
		return "", false
	}
	return owner, true
}
func (c *Console) scope(w http.ResponseWriter, r *http.Request) (f.Scope, bool) {
	owner, ok := c.owner(w, r)
	if !ok {
		return f.Scope{}, false
	}
	s := f.Scope{OwnerID: owner, AgentID: chi.URLParam(r, "agent")}
	if _, err := c.repo.GetAgent(r.Context(), s); err != nil {
		respondError(w, r, err)
		return s, false
	}
	s.WorkflowID = r.URL.Query().Get("workflow_id")
	if s.WorkflowID != "" {
		workflow, err := c.repo.GetWorkflow(r.Context(), s.OwnerID, s.WorkflowID)
		if err != nil {
			respondError(w, r, err)
			return s, false
		}
		if workflow.Status == "archived" {
			respondError(w, r, f.Fail("PERMISSION_DENIED", "工作流已归档"))
			return s, false
		}
	}
	return s, true
}
func (c *Console) listAgents(w http.ResponseWriter, r *http.Request) {
	owner, ok := c.owner(w, r)
	if !ok {
		return
	}
	items, err := c.repo.ListAgents(r.Context(), owner, 100, r.URL.Query().Get("after"))
	if err != nil {
		respondError(w, r, err)
		return
	}
	next := ""
	if len(items) == 100 {
		next = items[len(items)-1].ID
	}
	httpkit.WriteJSON(w, 200, map[string]any{"items": items, "next": next})
}

type agentInput struct {
	f.AgentDefinition
	Status string `json:"status"`
}

func (c *Console) validateAgent(a f.AgentDefinition) error {
	if len(a.Name) > 256 || len(a.Type) > 128 || len(a.SystemPrompt) > 64<<10 || len(a.Model.Model) > 256 {
		return f.Fail("INVALID_ARGUMENT", "名称、模型或提示词超长")
	}
	known := map[string]bool{}
	permissions := map[string]bool{}
	for _, t := range c.Service.Tools.Catalog() {
		known[t.Name] = true
		for _, p := range t.Permissions {
			permissions[p] = true
		}
	}
	for _, name := range a.EnabledTools {
		if !known[name] {
			return f.Fail("INVALID_ARGUMENT", "包含未注册的工具")
		}
	}
	for _, p := range a.Permissions {
		if !permissions[p] {
			return f.Fail("INVALID_ARGUMENT", "包含未知权限")
		}
	}
	provider := false
	for _, p := range c.Settings.View().Providers {
		if p.Name == a.Model.Provider {
			provider = true
		}
	}
	if a.Model.Provider == "demo" && c.Settings.demo {
		provider = true
	}
	if !provider {
		return f.Fail("INVALID_ARGUMENT", "请选择已配置的模型服务")
	}
	if a.Model.Temperature != nil && (*a.Model.Temperature < 0 || *a.Model.Temperature > 2) {
		return f.Fail("INVALID_ARGUMENT", "模型温度必须在 0 到 2 之间")
	}
	if a.Model.MaxTokens < 0 || a.Model.MaxTokens > 65536 {
		return f.Fail("INVALID_ARGUMENT", "模型输出 Token 限制无效")
	}
	l := a.Limits
	if l.ModelCalls < 0 || l.ToolCalls < 0 || l.DispatchDepth < 0 || l.DispatchDepth > 8 || l.MaxConcurrent < 0 || l.MaxConcurrent > 16 || l.ContextBytes < 0 || l.ContextBytes > 4<<20 || l.MemoryBytes < 0 || l.MemoryBytes > 128<<10 || l.ToolResultBytes < 0 || l.ToolResultBytes > 128<<10 || l.RunTimeout < 0 || l.RunTimeout > 24*time.Hour || l.ToolTimeout < 0 || l.ToolTimeout > 600*time.Second {
		return f.Fail("INVALID_ARGUMENT", "运行限制超出后台允许范围")
	}
	return nil
}
func (c *Console) saveAgent(w http.ResponseWriter, r *http.Request) {
	owner, ok := c.owner(w, r)
	if !ok {
		return
	}
	var input agentInput
	if httpkit.DecodeJSON(w, r, &input) != nil {
		return
	}
	if err := c.validateAgent(input.AgentDefinition); err != nil {
		respondError(w, r, err)
		return
	}
	if input.Status == "" {
		input.Status = "enabled"
	}
	id := chi.URLParam(r, "agent")
	status := 200
	if id == "" {
		id = f.NewID()
		status = 201
	} else {
		if _, err := c.repo.GetAgent(r.Context(), f.Scope{OwnerID: owner, AgentID: id}); err != nil {
			respondError(w, r, err)
			return
		}
	}
	// Keep manual HTTP runs below the host's response deadline; zero means console default.
	if input.Limits.RunTimeout == 0 {
		input.Limits.RunTimeout = 180 * time.Second
	}
	a, err := c.Service.Registry.Save(r.Context(), owner, f.AgentInstance{ID: id, AgentDefinition: input.AgentDefinition, Status: input.Status})
	if err != nil {
		respondError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, status, a)
}
func (c *Console) listMemories(w http.ResponseWriter, r *http.Request) {
	scope, ok := c.scope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, err := c.repo.SearchMemory(r.Context(), scope, f.MemorySearch{Query: q.Get("q"), Visibility: q.Get("visibility"), MemoryType: q.Get("memory_type"), Level: q.Get("level"), Status: q.Get("status"), Validity: q.Get("validity"), ParentID: q.Get("parent_memory_id"), Limit: 1000})
	if err != nil {
		respondError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"items": items, "limit": 1000})
}

func (c *Console) saveMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := c.scope(w, r)
	if !ok {
		return
	}
	var input json.RawMessage
	if httpkit.DecodeJSON(w, r, &input) != nil {
		return
	}
	id := chi.URLParam(r, "memory")
	existing := f.Memory{}
	if id != "" {
		var err error
		existing, err = c.repo.GetMemory(r.Context(), scope, id)
		if err != nil {
			respondError(w, r, err)
			return
		}
	}
	m, err := f.PatchMemory(existing, input)
	if err != nil {
		respondError(w, r, err)
		return
	}
	if id != "" && m.ID != id {
		respondError(w, r, f.Fail("INVALID_ARGUMENT", "记忆 ID 不匹配"))
		return
	}
	if id == "" && m.ID != "" {
		respondError(w, r, f.Fail("INVALID_ARGUMENT", "创建时不能指定记忆 ID"))
		return
	}
	m, err = c.repo.SaveMemory(r.Context(), scope, m, id == "")
	if err != nil {
		respondError(w, r, err)
		return
	}
	status := 200
	if id == "" {
		status = 201
	}
	httpkit.WriteJSON(w, status, m)
}
func (c *Console) memoryRelations(w http.ResponseWriter, r *http.Request) {
	scope, ok := c.scope(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "memory")
	if r.Method == "GET" {
		q := r.URL.Query()
		items, err := c.repo.GetMemoryRelations(r.Context(), scope, f.MemoryRelationQuery{MemoryID: id, Type: q.Get("relation_type"), Direction: q.Get("direction"), Limit: 100, Depth: 1})
		if err != nil {
			respondError(w, r, err)
			return
		}
		httpkit.WriteJSON(w, 200, map[string]any{"items": items})
		return
	}
	var input struct {
		TargetID    string `json:"target_memory_id"`
		Type        string `json:"relation_type"`
		Description string `json:"description"`
	}
	if httpkit.DecodeJSON(w, r, &input) != nil {
		return
	}
	relation, err := c.repo.CreateMemoryRelation(r.Context(), scope, f.MemoryRelation{SourceID: id, TargetID: input.TargetID, Type: input.Type, Description: input.Description})
	if err != nil {
		respondError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 201, relation)
}

func (c *Console) deleteMemory(w http.ResponseWriter, r *http.Request) {
	scope, ok := c.scope(w, r)
	if !ok {
		return
	}
	if err := c.repo.DeleteMemory(r.Context(), scope, chi.URLParam(r, "memory")); err != nil {
		respondError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 204, nil)
}
func (c *Console) runs(w http.ResponseWriter, r *http.Request) {
	scope, ok := c.scope(w, r)
	if !ok {
		return
	}
	items, err := c.adminRepo.ListAgentRuns(r.Context(), scope.OwnerID, scope.AgentID, 30)
	if err != nil {
		respondError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"items": items})
}
func (c *Console) run(w http.ResponseWriter, r *http.Request) {
	scope, ok := c.scope(w, r)
	if !ok {
		return
	}
	var input struct {
		Input json.RawMessage `json:"input"`
	}
	if httpkit.DecodeJSON(w, r, &input) != nil {
		return
	}
	if len(input.Input) == 0 || !json.Valid(input.Input) {
		httpkit.Error(w, r, 400, "INVALID_ARGUMENT", "请输入有效测试内容")
		return
	}
	payload, _ := json.Marshal(f.RunRequest{AgentID: scope.AgentID, Trigger: f.UserMessage, Input: input.Input})
	runCtx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer cancel()
	if scope.WorkflowID != "" {
		runCtx = f.WorkflowContext(runCtx, scope.OwnerID, scope.WorkflowID)
	}
	reply, err := c.Service.Call(runCtx, bridge.Request{Version: bridge.Version, OwnerID: scope.OwnerID, Operation: agent.RunOperation, Payload: payload})
	if err != nil {
		var failed f.RunResult
		_ = json.Unmarshal(reply.Payload, &failed)
		respondRunError(w, r, err, failed.RunID)
		return
	}
	var result f.RunResult
	if json.Unmarshal(reply.Payload, &result) != nil {
		respondError(w, r, f.Fail("INTERNAL_ERROR", "运行结果无效"))
		return
	}
	record, err := c.repo.GetRun(r.Context(), scope.OwnerID, result.RunID)
	if err != nil {
		respondError(w, r, err)
		return
	}
	httpkit.WriteJSON(w, 200, map[string]any{"result": result, "record": record})
}
