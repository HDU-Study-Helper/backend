package app

import (
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config contains process configuration, never request-controlled paths or credentials.
type Config struct {
	APIOnly           bool
	RefreshPaused     bool
	AgentDemoEnabled  bool
	DatabaseURL       string
	HTTPAddr          string
	PublicOrigin      string
	SecureCookies     bool
	BootstrapUsername string
	BootstrapPassword string
	CredentialKey     []byte
	DWSBinary         string
	DWSBinarySHA256   string
	DWSWorkspaceRoot  string
	DWSPollInterval   time.Duration
	DWSCommandTimeout time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: os.Getenv("HTTP_ADDR"), PublicOrigin: os.Getenv("PUBLIC_ORIGIN"), BootstrapUsername: os.Getenv("BOOTSTRAP_ADMIN_USERNAME"), BootstrapPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"), DWSBinary: os.Getenv("DWS_BINARY"), DWSBinarySHA256: os.Getenv("DWS_BINARY_SHA256"), DWSWorkspaceRoot: os.Getenv("DWS_WORKSPACE_ROOT"), DWSPollInterval: 10 * time.Minute, DWSCommandTimeout: 45 * time.Second}
	if raw := os.Getenv("API_ONLY"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return c, errors.New("invalid API_ONLY")
		}
		c.APIOnly = v
	}
	if raw := os.Getenv("REFRESH_PAUSED"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return c, errors.New("invalid REFRESH_PAUSED")
		}
		c.RefreshPaused = v
	}
	if raw := os.Getenv("AGENT_DEMO_ENABLED"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return c, errors.New("invalid AGENT_DEMO_ENABLED")
		}
		c.AgentDemoEnabled = v
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":2610"
	}
	if c.DWSWorkspaceRoot == "" {
		c.DWSWorkspaceRoot = "/dev/shm"
	}
	if raw := os.Getenv("COOKIE_SECURE"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return c, errors.New("invalid COOKIE_SECURE")
		}
		c.SecureCookies = v
	}
	if c.PublicOrigin != "" {
		u, err := url.Parse(c.PublicOrigin)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New("PUBLIC_ORIGIN must be an http(s) origin without path")
		}
		if u.Scheme == "https" && !c.SecureCookies {
			return c, errors.New("HTTPS PUBLIC_ORIGIN requires COOKIE_SECURE=true")
		}
	}
	if (c.BootstrapUsername == "") != (c.BootstrapPassword == "") {
		return c, errors.New("set both BOOTSTRAP_ADMIN_USERNAME and BOOTSTRAP_ADMIN_PASSWORD")
	}
	if raw := os.Getenv("CREDENTIAL_KEY"); raw != "" {
		v, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(v) != 32 {
			return c, errors.New("CREDENTIAL_KEY must be a base64 encoded 32-byte key")
		}
		c.CredentialKey = v
	}
	if c.DWSBinary != "" {
		if !filepath.IsAbs(c.DWSBinary) || c.DWSBinarySHA256 == "" || len(c.CredentialKey) != 32 {
			return c, errors.New("DWS requires an absolute DWS_BINARY, DWS_BINARY_SHA256 and CREDENTIAL_KEY")
		}
	}
	for _, setting := range []struct {
		name    string
		value   *time.Duration
		minimum time.Duration
	}{{"DWS_POLL_INTERVAL", &c.DWSPollInterval, 5 * time.Second}, {"REFRESH_INTERVAL", &c.DWSPollInterval, time.Minute}, {"DWS_COMMAND_TIMEOUT", &c.DWSCommandTimeout, time.Second}} {
		if raw := os.Getenv(setting.name); raw != "" {
			v, err := time.ParseDuration(raw)
			if err != nil || v < setting.minimum || v > 24*time.Hour {
				return c, errors.New("invalid " + setting.name)
			}
			*setting.value = v
		}
	}
	return c, nil
}
