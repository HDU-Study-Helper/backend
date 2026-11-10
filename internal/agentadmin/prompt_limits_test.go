package agentadmin

import (
	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"strings"
	"testing"
)

func TestConsoleAcceptsProfilePromptAndContextBudget(t *testing.T) {
	settings, _, _ := testSettings(t)
	c := Console{Settings: settings, Service: &agent.Service{Tools: f.NewToolRegistry()}}
	definition := f.AgentDefinition{SystemPrompt: strings.Repeat("课", 7000), Model: f.ModelConfig{Provider: settings.View().Providers[0].Name}, Limits: f.Limits{ContextBytes: 4 << 20, Unlimited: true}}
	if err := c.validateAgent(definition); err != nil {
		t.Fatal("profile Agent cannot be installed via public console", err)
	}
	definition.SystemPrompt = strings.Repeat("x", (64<<10)+1)
	if err := c.validateAgent(definition); err == nil {
		t.Fatal("oversized prompt accepted")
	}
	definition.SystemPrompt = "valid"
	definition.Limits.ContextBytes = (4 << 20) + 1
	if err := c.validateAgent(definition); err == nil {
		t.Fatal("oversized context accepted")
	}
}
