package app

import (
	"strings"
	"testing"
	"time"
)

func configEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"API_ONLY", "REFRESH_PAUSED", "AGENT_DEMO_ENABLED", "DATABASE_URL", "HTTP_ADDR", "PUBLIC_ORIGIN", "COOKIE_SECURE", "BOOTSTRAP_ADMIN_USERNAME", "BOOTSTRAP_ADMIN_PASSWORD", "CREDENTIAL_KEY", "DWS_BINARY", "DWS_BINARY_SHA256", "DWS_WORKSPACE_ROOT", "DWS_POLL_INTERVAL", "REFRESH_INTERVAL", "DWS_COMMAND_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://example")
}
func TestConfigDefaults(t *testing.T) {
	configEnv(t)
	c, err := LoadConfig()
	if err != nil || c.HTTPAddr != ":2610" || c.DWSWorkspaceRoot != "/dev/shm" || c.DWSPollInterval != 10*time.Minute {
		t.Fatalf("defaults: %v", err)
	}
}
func TestConfigRejectsUnsafeCombinations(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"PUBLIC_ORIGIN", "https://example.test/path"}, {"PUBLIC_ORIGIN", "https://example.test"}, {"COOKIE_SECURE", "maybe"}, {"BOOTSTRAP_ADMIN_USERNAME", "admin"}, {"CREDENTIAL_KEY", "short"}, {"DWS_BINARY", "/bin/anything"}, {"DWS_POLL_INTERVAL", "1ms"}, {"REFRESH_INTERVAL", "59s"}, {"DWS_COMMAND_TIMEOUT", "0s"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			configEnv(t)
			t.Setenv(tc.key, tc.value)
			if _, err := LoadConfig(); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
	configEnv(t)
	t.Setenv("PUBLIC_ORIGIN", "https://example.test")
	t.Setenv("COOKIE_SECURE", "true")
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
}
func TestConfigurationErrorsDoNotExposeSecrets(t *testing.T) {
	configEnv(t)
	secret := "private-not-base64"
	t.Setenv("CREDENTIAL_KEY", secret)
	_, err := LoadConfig()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestRefreshPausedConfiguration(t *testing.T) {
	configEnv(t)
	t.Setenv("REFRESH_PAUSED", "true")
	cfg, err := LoadConfig()
	if err != nil || !cfg.RefreshPaused {
		t.Fatal("paused startup missing", err)
	}
	t.Setenv("REFRESH_PAUSED", "invalid")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("invalid pause flag accepted")
	}
}

func TestAPIOnlyConfiguration(t *testing.T) {
	configEnv(t)
	cfg, err := LoadConfig()
	if err != nil || cfg.APIOnly {
		t.Fatal("API-only mode must be opt-in", err)
	}
	t.Setenv("API_ONLY", "true")
	cfg, err = LoadConfig()
	if err != nil || !cfg.APIOnly {
		t.Fatal("API-only mode not enabled", err)
	}
	t.Setenv("API_ONLY", "invalid")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("invalid API-only flag accepted")
	}
}

func TestSharedRefreshIntervalOverridesLegacyDWSSetting(t *testing.T) {
	configEnv(t)
	t.Setenv("DWS_POLL_INTERVAL", "2m")
	t.Setenv("REFRESH_INTERVAL", "10m")
	cfg, err := LoadConfig()
	if err != nil || cfg.DWSPollInterval != 10*time.Minute {
		t.Fatalf("shared interval: %+v %v", cfg.DWSPollInterval, err)
	}
}
