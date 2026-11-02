package agent_test

import (
	"context"
	"encoding/json"
	"os"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/dbbridge"
)

// ExampleNew is compiled but not run: it deliberately has no Output directive.
// The host supplies an existing authenticated owner's ID and its own notification service.
func ExampleNew() {
	ctx := context.Background()
	db, err := dbbridge.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		return
	}
	provider, err := f.NewOpenAIProvider(f.HTTPConfig{BaseURL: os.Getenv("MODEL_BASE_URL"), APIKey: os.Getenv("MODEL_API_KEY")})
	if err != nil {
		return
	}
	search, err := f.NewDeepSeekProvider(f.DeepSeekConfig{HTTPConfig: f.HTTPConfig{APIKey: os.Getenv("DEEPSEEK_API_KEY")}, Retries: 2})
	if err != nil {
		return
	}
	service, err := agent.New(agent.Dependencies{Repository: db.Agents(), Providers: map[string]f.ModelProvider{"chat": provider}, Search: search, Backend: agent.BackendServices{Data: db.AgentData()}})
	if err != nil {
		return
	}
	// Replace with the principal bound by the trusted application service, never model input.
	trustedOwner := "existing-authenticated-user-id"
	b, err := service.Registry.Create(ctx, trustedOwner, f.AgentDefinition{Name: "B", Type: "research", SystemPrompt: "Find relevant public sources, using web_search when needed.", Model: f.ModelConfig{Provider: "chat", Model: "configured-model-b"}, EnabledTools: []string{"web_search"}, Permissions: []string{"web.search"}})
	if err != nil {
		return
	}
	a, err := service.Registry.Create(ctx, trustedOwner, f.AgentDefinition{Name: "A", Type: "coordinator", SystemPrompt: "Delegate public research to the target agent. Save a workflow definition from its results.", Model: f.ModelConfig{Provider: "chat", Model: "configured-model-a"}, EnabledTools: []string{"agent_dispatch", "workflow_create"}, Permissions: []string{"agent.dispatch", "workflow.write"}})
	if err != nil {
		return
	}
	_, err = db.Agents().SaveMemory(ctx, f.Scope{OwnerID: trustedOwner, AgentID: a.ID}, f.Memory{Title: "Preference", Content: "Keep results concise"}, true)
	if err != nil {
		return
	}
	input, _ := json.Marshal(map[string]string{"task": "Find a public reference and save its workflow definition", "target_agent_id": b.ID})
	result, err := service.Run(ctx, trustedOwner, f.RunRequest{AgentID: a.ID, Trigger: f.SystemEvent, Input: input})
	_, _ = result, err
}
