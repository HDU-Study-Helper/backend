package agentadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/agent"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
)

type settingsRepo struct {
	model.AgentAdminRepository
	mu      sync.Mutex
	raw     []byte
	version int
}

func (r *settingsRepo) LoadAgentSettings(context.Context) ([]byte, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.version == 0 {
		return nil, 0, model.ErrNotFound
	}
	return append([]byte(nil), r.raw...), r.version, nil
}
func (r *settingsRepo) SaveAgentSettings(_ context.Context, raw []byte, version int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if version != r.version {
		return 0, model.ErrConflict
	}
	r.raw = append([]byte(nil), raw...)
	r.version++
	return r.version, nil
}
func testSettings(t *testing.T) (*SettingsManager, *settingsRepo, *credential.Vault) {
	t.Helper()
	vault, err := credential.New(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repo := &settingsRepo{}
	s, err := NewSettings(context.Background(), repo, vault, true)
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, vault
}
func TestSettingsEncryptedRetainedClearedAndReloaded(t *testing.T) {
	s, repo, vault := testSettings(t)
	config := s.View()
	config.Providers[0].APIKey = "private-api-key-123"
	config.Search.APIKey = "private-search-key-456"
	view, err := s.Save(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Providers[0].HasAPIKey || view.Providers[0].APIKey != "" || view.Search.APIKey != "" || !view.Search.HasAPIKey {
		t.Fatal("secret disclosure in view")
	}
	raw, _ := json.Marshal(view)
	if bytes.Contains(raw, []byte("private-")) || bytes.Contains(repo.raw, []byte("private-")) {
		t.Fatal("plaintext key persisted or returned")
	}
	if s.ResolveProvider("default") == nil || s.ResolveProvider("demo") == nil {
		t.Fatal("provider not configured")
	}
	oldProvider := s.ResolveProvider("default")
	view.Providers[0].DefaultModel = "new-model"
	view.Providers[0].APIProtocol = "responses"
	view, err = s.Save(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Providers[0].HasAPIKey || s.ResolveProvider("default") == oldProvider {
		t.Fatal("hot provider configuration failed")
	}
	restored, err := NewSettings(context.Background(), repo, vault, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.View().Providers[0].DefaultModel != "new-model" || restored.View().Providers[0].APIProtocol != "responses" || restored.ResolveProvider("default") == nil || restored.ResolveProvider("demo") != nil {
		t.Fatal("restart persistence failed")
	}
	_, err = s.Save(context.Background(), config)
	if err == nil || f.SafeError(err).Code != "CONFIG_CONFLICT" {
		t.Fatal("stale settings accepted")
	}
	view.Providers[0].ClearAPIKey = true
	view.Search.ClearAPIKey = true
	view, err = s.Save(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	if view.Providers[0].HasAPIKey || view.Search.HasAPIKey || s.ResolveProvider("default") != nil {
		t.Fatal("key clearing failed")
	}
}
func TestSettingsValidationAndMissingVault(t *testing.T) {
	s, _, _ := testSettings(t)
	for _, mutate := range []func(*Settings){func(c *Settings) { c.Providers[0].Name = "demo" }, func(c *Settings) { c.Providers = append(c.Providers, c.Providers[0]) }, func(c *Settings) { c.Providers[0].BaseURL = "https://user:secret@example.com" }, func(c *Settings) { c.Search.Retries = 4 }, func(c *Settings) { c.Providers[0].APIKey = "key\r\ninjected" }} {
		config := s.View()
		mutate(&config)
		if _, err := s.Save(context.Background(), config); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
	noKey, err := NewSettings(context.Background(), &settingsRepo{}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if noKey.View().Writable {
		t.Fatal("unavailable vault reported writable")
	}
	if _, err = noKey.Save(context.Background(), noKey.View()); err == nil || f.SafeError(err).Code != "NOT_IMPLEMENTED" {
		t.Fatal("unencrypted save accepted")
	}
}
func TestModelInheritanceSurvivesDefaultChangeAndRestart(t *testing.T) {
	s, repo, vault := testSettings(t)
	view := s.View()
	view.Providers[0].APIKey = "fixture-key"
	view, err := s.Save(context.Background(), view)
	if err != nil {
		t.Fatal(err)
	}
	legacy := f.ModelConfig{Provider: "default", Model: "deepseek-chat"}
	custom := f.ModelConfig{Provider: "default", Model: "deepseek-chat", Mode: "custom"}
	inherited := f.ModelConfig{Provider: "default", Mode: "default"}
	view.Providers[0].DefaultModel = "new-model"
	if _, err = s.Save(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	restored, err := NewSettings(context.Background(), repo, vault, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, manager := range []*SettingsManager{s, restored} {
		for _, config := range []f.ModelConfig{legacy, inherited} {
			_, effective, e := manager.ResolveModel(config)
			if e != nil || effective.Model != "new-model" {
				t.Fatalf("inheritance: %#v %v", effective, e)
			}
		}
		_, effective, e := manager.ResolveModel(custom)
		if e != nil || effective.Model != "deepseek-chat" {
			t.Fatal("custom overwritten", e)
		}
		_, effective, e = manager.ResolveModel(f.ModelConfig{Provider: "default", Model: "other-custom"})
		if e != nil || effective.Model != "other-custom" {
			t.Fatal("legacy custom overwritten", e)
		}
	}
	view = s.View()
	view.Providers[0].ClearAPIKey = true
	if _, err = s.Save(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ResolveModel(inherited); err == nil || !bytes.Contains([]byte(err.Error()), []byte("API Key")) {
		t.Fatal("missing-key error unclear", err)
	}
	view = s.View()
	view.Providers = nil
	if _, err = s.Save(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ResolveModel(inherited); err == nil || !bytes.Contains([]byte(err.Error()), []byte("删除")) {
		t.Fatal("removed-provider error unclear", err)
	}
}

func TestLongModelRequestPolicyPersistsAndUnlimitedAgentValidates(t *testing.T) {
	s, repo, vault := testSettings(t)
	config := s.View()
	config.Providers[0].TimeoutSeconds = 600
	config.Providers[0].MaxConcurrent = 2
	config.Providers[0].Retries = 3
	if _, err := s.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	restored, err := NewSettings(context.Background(), repo, vault, false)
	if err != nil {
		t.Fatal(err)
	}
	p := restored.View().Providers[0]
	if p.TimeoutSeconds != 600 || p.MaxConcurrent != 2 || p.Retries != 3 {
		t.Fatal(p)
	}
	c := Console{Settings: s, Service: &agent.Service{Tools: f.NewToolRegistry()}}
	a := f.AgentDefinition{Model: f.ModelConfig{Provider: p.Name}, Limits: f.Limits{Unlimited: true}}
	if err := c.validateAgent(a); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Settings){func(v *Settings) { v.Providers[0].TimeoutSeconds = 601 }, func(v *Settings) { v.Providers[0].MaxConcurrent = 33 }, func(v *Settings) { v.Providers[0].Retries = 6 }} {
		bad := s.View()
		mutate(&bad)
		if _, err := s.Save(context.Background(), bad); err == nil {
			t.Fatal("invalid model request policy accepted")
		}
	}
}

func TestDemoProviderCallsActualMemoryTool(t *testing.T) {
	provider := DemoProvider{}
	reply, err := provider.Complete(context.Background(), f.ModelRequest{Tools: []f.ToolSpec{{Name: "memory_list"}}, Messages: []f.Message{{Role: "user", Content: `{"input":{"text":"test"}}`}}})
	if err != nil || len(reply.Message.ToolCalls) != 1 || reply.Message.ToolCalls[0].Function.Name != "memory_list" {
		t.Fatal(reply, err)
	}
	reply, err = provider.Complete(context.Background(), f.ModelRequest{Messages: []f.Message{{Role: "tool", Content: `{"ok":true,"data":[{"title":"remembered"}],"error":null}`}}})
	if err != nil || reply.FinishReason != "stop" || !bytes.Contains([]byte(reply.Message.Content), []byte("local_demo")) {
		t.Fatal(reply, err)
	}
}
