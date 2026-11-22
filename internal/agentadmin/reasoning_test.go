package agentadmin

import (
	"context"
	"encoding/json"
	"testing"

	f "academic/agent/framework"
)

func TestReasoningUsesOneRuleForAllModels(t *testing.T) {
	for _, model := range []string{"gpt-6-luna", "other-model"} {
		for _, tc := range []struct{ defaults, override, want string }{
			{"", "", ""}, {"high", "", `"high"`}, {"off", "", "null"},
			{"high", "null", "null"}, {"max", `"low"`, `"low"`},
		} {
			original := map[string]json.RawMessage{"seed": json.RawMessage(`7`)}
			if tc.override != "" {
				original["reasoning_effort"] = json.RawMessage(tc.override)
			}
			got, err := resolveReasoningEffort(f.ModelConfig{Model: model, Parameters: original}, tc.defaults)
			if err != nil || string(got.Parameters["reasoning_effort"]) != tc.want {
				t.Fatal(model, tc, got.Parameters, err)
			}
			got.Parameters["seed"][0] = '8'
			if string(original["seed"]) != "7" || string(original["reasoning_effort"]) != tc.override {
				t.Fatal("modified stored parameters")
			}
		}
	}
}

func TestServiceReasoningDefaultPersistsAndSupportsFixedModels(t *testing.T) {
	ctx := context.Background()
	s, repo, vault := testSettings(t)
	cfg := s.View()
	cfg.Providers[0].APIKey = "test-key"
	cfg.Providers[0].DefaultModel = "gpt-6-luna"
	cfg.Providers[0].DefaultReasoningEffort = "low"
	cfg, err := s.Save(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewSettings(ctx, repo, vault, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.View().Providers[0].DefaultReasoningEffort != "low" {
		t.Fatal("default not saved")
	}
	for _, c := range []f.ModelConfig{{Provider: "@configured", Mode: "default"}, {Provider: "default", Mode: "custom", Model: "other-model"}} {
		_, got, err := s.ResolveModel(c)
		if err != nil || string(got.Parameters["reasoning_effort"]) != `"low"` {
			t.Fatal(got, err)
		}
	}
	cfg.Providers[0].DefaultReasoningEffort = "invalid"
	if _, err := s.Save(ctx, cfg); err == nil {
		t.Fatal("invalid provider default accepted")
	}
}
