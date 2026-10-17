package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}
	if cfg.Search.DefaultCount != 10 || cfg.Search.MaxCount != 50 {
		t.Errorf("unexpected defaults: %+v", cfg.Search)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
server:
  addr: ":9090"
  request_timeout: 20s
auth:
  mode: key
  keys:
    - name: me
      token: "plain-text-token"
      rpm: 10
      burst: 3
      daily_quota: 100
      max_results: 7
search:
  default_count: 5
  max_count: 30
providers:
  - name: bing
    type: bingrss
    base_url: "https://cn.bing.com"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("addr = %q", cfg.Server.Addr)
	}
	if cfg.Server.RequestTimeout.D() != 20*time.Second {
		t.Errorf("request_timeout = %v", cfg.Server.RequestTimeout.D())
	}
	if cfg.Auth.Mode != "key" || len(cfg.Auth.Keys) != 1 {
		t.Fatalf("auth = %+v", cfg.Auth)
	}
	k := cfg.Auth.Keys[0]
	if k.Name != "me" || k.Token != "" || !strings.HasPrefix(k.HashedToken, "sha256:") {
		t.Errorf("key normalization failed: %+v", k)
	}
	if k.Burst != 3 || k.DailyQuota != 100 || k.MaxResults != 7 {
		t.Errorf("key limits = %+v", k)
	}
	if cfg.Search.DefaultCount != 5 || cfg.Search.MaxCount != 30 {
		t.Errorf("search defaults = %+v", cfg.Search)
	}
}

func TestHashToken(t *testing.T) {
	a := HashToken("secret")
	b := HashToken("secret")
	if a != b {
		t.Error("HashToken must be deterministic")
	}
	if !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 {
		t.Errorf("unexpected hash format: %q", a)
	}
	if HashToken("other") == a {
		t.Error("different tokens must hash differently")
	}
	pre := "sha256:" + strings.Repeat("AB", 32)
	got := HashToken(pre)
	if got != "sha256:"+strings.Repeat("ab", 32) {
		t.Errorf("sha256: input should be normalized, got %q", got)
	}
}

func TestInvalidConfigs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"mode key without keys", func(c *Config) { c.Auth.Mode = "key"; c.Auth.Keys = nil }, "no auth.keys"},
		{"bad auth mode", func(c *Config) { c.Auth.Mode = "banana" }, "auth.mode"},
		{"unknown provider", func(c *Config) { c.Providers = []ProviderConfig{{Name: "x", Type: "nope", BaseURL: "https://x"}} }, "unknown type"},
		{"no providers", func(c *Config) { c.Providers = nil }, "no enabled search providers"},
		{"brave without key", func(c *Config) { c.Providers = []ProviderConfig{{Name: "b", Type: "brave"}} }, "api_key is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(cfg)
			if err := cfg.Normalize(); err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("ONESEARCH_ADDR", ":7777")
	t.Setenv("ONESEARCH_AUTH_MODE", "key")
	t.Setenv("ONESEARCH_AUTH_KEYS", "alice:tok-a:30:500:10,bob:tok-b")
	t.Setenv("ONESEARCH_SEARXNG_URL", "http://10.0.0.5:8888")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != ":7777" {
		t.Errorf("addr = %q", cfg.Server.Addr)
	}
	if cfg.Auth.Mode != "key" || len(cfg.Auth.Keys) != 2 {
		t.Fatalf("keys = %+v", cfg.Auth.Keys)
	}
	if cfg.Auth.Keys[0].Name != "alice" || cfg.Auth.Keys[0].RPM != 30 || cfg.Auth.Keys[0].DailyQuota != 500 || cfg.Auth.Keys[0].MaxResults != 10 {
		t.Errorf("alice = %+v", cfg.Auth.Keys[0])
	}
	found := false
	for _, p := range cfg.Providers {
		if p.Type == "searxng" && p.BaseURL == "http://10.0.0.5:8888" {
			found = true
		}
	}
	if !found {
		t.Errorf("searxng provider not overridden: %+v", cfg.Providers)
	}
}

func TestMissingExplicitConfigFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected error for explicitly supplied missing config")
	}
}

func TestDurationUnmarshalNumberAsSeconds(t *testing.T) {
	var d Duration
	if err := d.UnmarshalYAML(yamlNode(t, "90")); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.D() != 90*time.Second {
		t.Errorf("duration = %v, want 90s", d.D())
	}
}
