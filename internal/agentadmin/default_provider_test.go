package agentadmin

import (
	"context"
	"testing"

	f "academic/agent/framework"
)

func TestFrameworkDefaultProviderSelection(t *testing.T) {
	ctx := context.Background()
	s, repo, vault := testSettings(t)
	cfg := s.View()
	cfg.Providers[0].APIKey = "first-key"
	other := cfg.Providers[0]
	other.Name, other.APIKey, other.DefaultModel, other.DefaultReasoningEffort = "second", "second-key", "second-model", "low"
	cfg.Providers = append(cfg.Providers, other)
	cfg, err := s.Save(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	infer := f.ModelConfig{Provider: "@configured", Mode: "default"}
	if _, _, err = s.ResolveModel(infer); err == nil || f.SafeError(err).Code != "MODEL_SERVICE_SELECTION_REQUIRED" {
		t.Fatal("ambiguous default should not use list order", err)
	}
	cfg.DefaultProvider = "second"
	cfg, err = s.Save(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewSettings(ctx, repo, vault, false)
	if err != nil || restored.View().DefaultProvider != "second" {
		t.Fatal("failed to reload selected default", err)
	}
	_, effective, err := restored.ResolveModel(infer)
	if err != nil || effective.Provider != "second" || effective.Model != "second-model" || string(effective.Parameters["reasoning_effort"]) != `"low"` {
		t.Fatal(effective, err)
	}
	_, fixed, err := restored.ResolveModel(f.ModelConfig{Provider: "default", Mode: "custom", Model: "pinned"})
	if err != nil || fixed.Provider != "default" || fixed.Model != "pinned" {
		t.Fatal(fixed, err)
	}
	cfg.Providers[1].ClearAPIKey = true
	if _, err = restored.Save(ctx, cfg); err == nil || f.SafeError(err).Code != "INVALID_ARGUMENT" {
		t.Fatal("accepted removal of selected credential", err)
	}
	cfg = restored.View()
	cfg.DefaultProvider = ""
	cfg.Providers[1].ClearAPIKey = true
	if _, err = restored.Save(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	_, effective, err = restored.ResolveModel(infer)
	if err != nil || effective.Provider != "default" {
		t.Fatal("lone service compatibility", effective, err)
	}
}
