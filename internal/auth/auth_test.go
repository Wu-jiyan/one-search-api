package auth

import (
	"strings"
	"testing"

	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/httpx"
)

func testAuthConfig() config.AuthConfig {
	return config.AuthConfig{
		Mode: "key",
		Keys: []config.KeyConfig{{
			Name:        "tester",
			HashedToken: config.HashToken("tok-1"),
			RPM:         1000,
			Burst:       100,
			DailyQuota:  2,
			MaxResults:  5,
		}},
	}
}

func TestAuthenticate(t *testing.T) {
	a := New(testAuthConfig())
	key, apiErr := a.Authenticate("tok-1", "1.1.1.1")
	if apiErr != nil {
		t.Fatalf("Authenticate: %v", apiErr)
	}
	if key.Name != "tester" {
		t.Errorf("key.Name = %q", key.Name)
	}
	if _, apiErr := a.Authenticate("wrong", "1.1.1.1"); apiErr == nil || apiErr.Status != 401 {
		t.Errorf("wrong token: %v", apiErr)
	}
	if _, apiErr := a.Authenticate("", "1.1.1.1"); apiErr == nil || apiErr.Status != 401 {
		t.Errorf("missing token: %v", apiErr)
	}
}

func TestDailyQuota(t *testing.T) {
	a := New(testAuthConfig())
	key, _ := a.Authenticate("tok-1", "1.1.1.1")
	for i := 0; i < 2; i++ {
		if apiErr := key.Admit(); apiErr != nil {
			t.Fatalf("Admit #%d: %v", i+1, apiErr)
		}
	}
	apiErr := key.Admit()
	if apiErr == nil || apiErr.Status != 402 {
		t.Fatalf("third Admit = %v, want 402 quota error", apiErr)
	}
	if !strings.Contains(apiErr.Message, "quota") {
		t.Errorf("message = %q", apiErr.Message)
	}
}

func TestRPMLimit(t *testing.T) {
	cfg := config.AuthConfig{Mode: "key", Keys: []config.KeyConfig{{
		Name: "fast", HashedToken: config.HashToken("tok"), RPM: 1, Burst: 1,
	}}}
	a := New(cfg)
	key, _ := a.Authenticate("tok", "1.1.1.1")
	if apiErr := key.Admit(); apiErr != nil {
		t.Fatalf("first Admit: %v", apiErr)
	}
	apiErr := key.Admit()
	if apiErr == nil || apiErr.Status != 429 {
		t.Fatalf("second Admit = %v, want 429", apiErr)
	}
	if apiErr.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want > 0", apiErr.RetryAfter)
	}
}

func TestAnonymousMode(t *testing.T) {
	cfg := config.AuthConfig{Mode: "none", AnonymousRPM: 100, AnonymousDailyQuota: 3}
	a := New(cfg)

	k1, apiErr := a.Authenticate("", "1.1.1.1")
	if apiErr != nil {
		t.Fatalf("anonymous: %v", apiErr)
	}
	k2, _ := a.Authenticate("", "1.1.1.1")
	if k1 != k2 {
		t.Error("same IP should map to the same anonymous key")
	}
	k3, _ := a.Authenticate("", "2.2.2.2")
	if k3 == k1 {
		t.Error("different IPs should map to different anonymous keys")
	}
}

func TestAllowedProviders(t *testing.T) {
	cfg := config.AuthConfig{Mode: "key", Keys: []config.KeyConfig{{
		Name: "p", HashedToken: config.HashToken("tok"), RPM: 10, Burst: 10,
		AllowedProviders: []string{"searxng", "bing"},
	}}}
	a := New(cfg)
	key, _ := a.Authenticate("tok", "x")
	if !key.AllowedProvider("searxng") || !key.AllowedProvider("bing") {
		t.Error("listed providers should be allowed")
	}
	if key.AllowedProvider("brave") {
		t.Error("unlisted provider must be denied")
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	e := httpx.RateLimited("slow down", 0)
	if e.Status != 429 || e.Type != httpx.TypeRateLimit {
		t.Errorf("unexpected error: %+v", e)
	}
	if e.Code != "rate_limit_exceeded" {
		t.Errorf("code = %q", e.Code)
	}
}
