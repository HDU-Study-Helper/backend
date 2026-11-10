// Package agentadmin provides the temporary backend management console, outside the framework.
package agentadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/credential"
	"academic/backend/internal/model"
)

type ProviderSettings struct {
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	DefaultModel   string `json:"default_model"`
	APIKey         string `json:"api_key,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxConcurrent  int    `json:"max_concurrent"`
	Retries        int    `json:"retries"`
	HasAPIKey      bool   `json:"has_api_key"`
	ClearAPIKey    bool   `json:"clear_api_key,omitempty"`
}
type SearchSettings struct {
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key,omitempty"`
	HasAPIKey      bool   `json:"has_api_key"`
	ClearAPIKey    bool   `json:"clear_api_key,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Retries        int    `json:"retries"`
}
type Settings struct {
	// Server-maintained legacy defaults, encrypted with settings; never accepted from clients.
	LegacyModels map[string][]string `json:"legacy_models,omitempty"`
	Providers    []ProviderSettings  `json:"providers"`
	Search       SearchSettings      `json:"search"`
	Version      int                 `json:"version"`
	Writable     bool                `json:"writable"`
	DemoEnabled  bool                `json:"demo_enabled"`
}
type SettingsManager struct {
	mu        sync.RWMutex
	writeMu   sync.Mutex
	repo      model.AgentAdminRepository
	vault     *credential.Vault
	settings  Settings
	providers map[string]f.ModelProvider
	search    f.SearchProvider
	demo      bool
}

func NewSettings(ctx context.Context, repo model.AgentAdminRepository, vault *credential.Vault, demo bool) (*SettingsManager, error) {
	s := &SettingsManager{repo: repo, vault: vault, demo: demo, providers: map[string]f.ModelProvider{}, settings: Settings{Providers: []ProviderSettings{{Name: "default", BaseURL: "https://api.deepseek.com", DefaultModel: "deepseek-chat", TimeoutSeconds: 600, MaxConcurrent: 2, Retries: 3}}, Search: SearchSettings{BaseURL: "https://api.deepseek.com", TimeoutSeconds: 20, Retries: 2}}}
	raw, version, err := repo.LoadAgentSettings(ctx)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, errors.New("agent settings could not be loaded")
	}
	if err == nil {
		if vault == nil {
			return nil, errors.New("CREDENTIAL_KEY required to decrypt saved agent settings")
		}
		plain, e := vault.Open("agent-framework", "settings", raw)
		if e != nil {
			return nil, errors.New("agent settings decryption failed")
		}
		defer clear(plain)
		if json.Unmarshal(plain, &s.settings) != nil {
			return nil, errors.New("invalid stored agent settings")
		}
		s.settings.Version = version
	}
	for i := range s.settings.Providers {
		if s.settings.Providers[i].MaxConcurrent == 0 {
			s.settings.Providers[i].MaxConcurrent = 2
		}
	}
	providers, search, err := s.build(s.settings)
	if err != nil {
		return nil, err
	}
	s.providers = providers
	s.search = search
	return s, nil
}
func endpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https") && len(raw) <= 2048
}
func (s *SettingsManager) build(config Settings) (map[string]f.ModelProvider, f.SearchProvider, error) {
	if len(config.Providers) > 16 {
		return nil, nil, f.Fail("INVALID_ARGUMENT", "最多配置 16 个模型服务")
	}
	providers := map[string]f.ModelProvider{}
	names := map[string]bool{}
	for _, p := range config.Providers {
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(p.Name) || p.Name == "demo" || names[p.Name] || !endpoint(p.BaseURL) || len(p.APIKey) > 4096 || strings.ContainsAny(p.APIKey, "\r\n") || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 600 || p.MaxConcurrent < 0 || p.MaxConcurrent > 32 || p.Retries < 0 || p.Retries > 5 || len(p.DefaultModel) > 256 {
			return nil, nil, f.Fail("INVALID_ARGUMENT", "模型服务名称、地址或超时配置无效")
		}
		names[p.Name] = true
		if p.APIKey != "" {
			provider, err := f.NewOpenAIProvider(f.HTTPConfig{BaseURL: p.BaseURL, APIKey: p.APIKey, Timeout: time.Duration(p.TimeoutSeconds) * time.Second, MaxConcurrent: p.MaxConcurrent, Retries: p.Retries})
			if err != nil {
				return nil, nil, err
			}
			providers[p.Name] = provider
		}
	}
	c := config.Search
	if !endpoint(c.BaseURL) || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60 || c.Retries < 0 || c.Retries > 3 || len(c.APIKey) > 4096 || strings.ContainsAny(c.APIKey, "\r\n") {
		return nil, nil, f.Fail("INVALID_ARGUMENT", "搜索服务地址、超时或重试配置无效")
	}
	var search f.SearchProvider
	if c.APIKey != "" {
		var err error
		search, err = f.NewDeepSeekProvider(f.DeepSeekConfig{HTTPConfig: f.HTTPConfig{BaseURL: c.BaseURL, APIKey: c.APIKey, Timeout: time.Duration(c.TimeoutSeconds) * time.Second}, Retries: c.Retries})
		if err != nil {
			return nil, nil, err
		}
	}
	return providers, search, nil
}
func (s *SettingsManager) View() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.settings
	out.LegacyModels = make(map[string][]string)
	for name, models := range s.settings.LegacyModels {
		out.LegacyModels[name] = append([]string(nil), models...)
	}
	out.Providers = append([]ProviderSettings{}, s.settings.Providers...)
	for i := range out.Providers {
		out.Providers[i].HasAPIKey = out.Providers[i].APIKey != ""
		out.Providers[i].APIKey = ""
		out.Providers[i].ClearAPIKey = false
	}
	out.Search.HasAPIKey = out.Search.APIKey != ""
	out.Search.APIKey = ""
	out.Search.ClearAPIKey = false
	out.Writable = s.vault != nil
	out.DemoEnabled = s.demo
	return out
}
func (s *SettingsManager) Save(ctx context.Context, input Settings) (Settings, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.vault == nil {
		return Settings{}, f.Fail("NOT_IMPLEMENTED", "服务端尚未配置 CREDENTIAL_KEY，无法加密保存 API Key")
	}
	s.mu.RLock()
	old := s.settings
	s.mu.RUnlock()
	if input.Version != old.Version {
		return Settings{}, f.Fail("CONFIG_CONFLICT", "配置已更新，请刷新后重试")
	}
	for i := range input.Providers {
		p := &input.Providers[i]
		if p.MaxConcurrent == 0 {
			p.MaxConcurrent = 2
		}
		if p.ClearAPIKey {
			p.APIKey = ""
		} else if p.APIKey == "" {
			for _, previous := range old.Providers {
				if previous.Name == p.Name {
					p.APIKey = previous.APIKey
				}
			}
		}
		p.HasAPIKey = false
		p.ClearAPIKey = false
	}
	if input.Search.ClearAPIKey {
		input.Search.APIKey = ""
	} else if input.Search.APIKey == "" {
		input.Search.APIKey = old.Search.APIKey
	}
	input.LegacyModels = make(map[string][]string)
	for name, models := range old.LegacyModels {
		input.LegacyModels[name] = append([]string(nil), models...)
	}
	for _, p := range old.Providers {
		if p.DefaultModel == "" {
			continue
		}
		found := false
		for _, name := range input.LegacyModels[p.Name] {
			if name == p.DefaultModel {
				found = true
			}
		}
		if !found {
			input.LegacyModels[p.Name] = append(input.LegacyModels[p.Name], p.DefaultModel)
		}
	}
	input.Search.HasAPIKey = false
	input.Search.ClearAPIKey = false
	input.Writable = false
	input.DemoEnabled = false
	providers, search, err := s.build(input)
	if err != nil {
		return Settings{}, err
	}
	plain, err := json.Marshal(input)
	if err != nil {
		return Settings{}, err
	}
	defer clear(plain)
	encrypted, err := s.vault.Seal("agent-framework", "settings", plain)
	if err != nil {
		return Settings{}, f.Fail("INTERNAL_ERROR", "配置加密失败")
	}
	version, err := s.repo.SaveAgentSettings(ctx, encrypted, input.Version)
	if errors.Is(err, model.ErrConflict) {
		return Settings{}, f.Fail("CONFIG_CONFLICT", "配置已更新，请刷新后重试")
	}
	if err != nil {
		return Settings{}, err
	}
	input.Version = version
	s.mu.Lock()
	s.settings = input
	s.providers = providers
	s.search = search
	s.mu.Unlock()
	return s.View(), nil
}
func (s *SettingsManager) ResolveProvider(name string) f.ModelProvider {
	if name == "demo" && s.demo {
		return DemoProvider{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providers[name]
}

// ResolveModel snapshots provider credentials and default model together. Legacy models
// matching a current or former default inherit; explicit custom always stays pinned.
func (s *SettingsManager) ResolveModel(config f.ModelConfig) (f.ModelProvider, f.ModelConfig, error) {
	if config.Provider == "demo" && s.demo {
		return DemoProvider{}, config, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var settings *ProviderSettings
	for i := range s.settings.Providers {
		if s.settings.Providers[i].Name == config.Provider {
			settings = &s.settings.Providers[i]
			break
		}
	}
	if settings == nil {
		return nil, config, f.Fail("PROVIDER_ERROR", "模型服务已删除或未配置，请重新选择模型服务")
	}
	provider := s.providers[config.Provider]
	if provider == nil {
		return nil, config, f.Fail("PROVIDER_ERROR", "模型服务尚未配置 API Key，请在框架配置中保存密钥")
	}
	if config.Mode != "" && config.Mode != "default" && config.Mode != "custom" {
		return nil, config, f.Fail("INVALID_ARGUMENT", "模型模式无效")
	}
	inherit := config.Mode == "default" || (config.Mode == "" && (config.Model == "" || config.Model == settings.DefaultModel))
	if config.Mode == "" {
		for _, previous := range s.settings.LegacyModels[config.Provider] {
			inherit = inherit || previous == config.Model
		}
	}
	if inherit {
		config.Model = settings.DefaultModel
		config.Mode = "default"
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, config, f.Fail("PROVIDER_ERROR", "模型服务未设置默认模型，请配置默认模型或选择自定义模型")
	}
	return provider, config, nil
}

// Search methods snapshot one immutable provider so config changes do not race in-flight requests.
func (s *SettingsManager) Search(ctx context.Context, r f.SearchRequest) ([]f.SearchResult, error) {
	s.mu.RLock()
	p := s.search
	s.mu.RUnlock()
	if p == nil {
		return nil, f.Fail("NOT_IMPLEMENTED", "请先配置搜索 API Key")
	}
	return p.Search(ctx, r)
}
func (s *SettingsManager) Fetch(ctx context.Context, u string) (f.Page, error) {
	s.mu.RLock()
	p := s.search
	s.mu.RUnlock()
	if p == nil {
		return f.Page{}, f.Fail("NOT_IMPLEMENTED", "请先配置搜索 API Key")
	}
	return p.Fetch(ctx, u)
}

type DemoProvider struct{}

func (DemoProvider) Complete(ctx context.Context, r f.ModelRequest) (f.ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return f.ModelResponse{}, err
	}
	var lastTool string
	for _, m := range r.Messages {
		if m.Role == "tool" {
			lastTool = m.Content
		}
	}
	if lastTool == "" {
		for _, tool := range r.Tools {
			if tool.Name == "memory_list" {
				return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "demo-memory", Type: "function", Function: f.FunctionCall{Name: "memory_list", Arguments: `{"limit":20}`}}}}, FinishReason: "tool_calls"}, nil
			}
		}
	}
	var input json.RawMessage
	for _, m := range r.Messages {
		if m.Role == "user" {
			var envelope struct {
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal([]byte(m.Content), &envelope) == nil && len(envelope.Input) > 0 {
				input = envelope.Input
			}
		}
	}
	output, _ := json.MarshalIndent(map[string]any{"mode": "local_demo", "message": "本地自测完成，未调用外部模型。", "input": input, "memory_tool_result": json.RawMessage(lastToolOrNull(lastTool))}, "", "  ")
	return f.ModelResponse{Message: f.Message{Role: "assistant", Content: string(output)}, FinishReason: "stop"}, nil
}
func lastToolOrNull(value string) string {
	if value == "" {
		return "null"
	}
	return value
}
