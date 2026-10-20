// Package config loads, normalizes, and validates the service configuration.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a YAML-friendly wrapper around time.Duration. It accepts either a
// duration string ("5s", "1m") or a bare number interpreted as seconds.
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Tag == "!!int" || value.Tag == "!!float" {
		var n float64
		if err := value.Decode(&n); err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		*d = Duration(time.Duration(n * float64(time.Second)))
		return nil
	}
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"5s\" or a number of seconds: %w", err)
	}
	v, perr := time.ParseDuration(strings.TrimSpace(s))
	if perr != nil {
		return fmt.Errorf("invalid duration %q: %w", s, perr)
	}
	*d = Duration(v)
	return nil
}

// Config is the root configuration document.
type Config struct {
	Server    ServerConfig     `yaml:"server"`
	Auth      AuthConfig       `yaml:"auth"`
	Search    SearchConfig     `yaml:"search"`
	Fetch     FetchConfig      `yaml:"fetch"`
	Providers []ProviderConfig `yaml:"providers"`
	Cache     CacheConfig      `yaml:"cache"`
	Log       LogConfig        `yaml:"log"`
}

// ServerConfig covers the HTTP listener and request envelope.
type ServerConfig struct {
	Addr              string   `yaml:"addr"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	RequestTimeout    Duration `yaml:"request_timeout"`
	ShutdownTimeout   Duration `yaml:"shutdown_timeout"`
	MaxBodyBytes      int64    `yaml:"max_body_bytes"`
	TrustedProxies    []string `yaml:"trusted_proxies"`
}

// AuthConfig controls API key authentication.
type AuthConfig struct {
	Mode string `yaml:"mode"` // "key" | "none"
	// AnonymousRPM and AnonymousDailyQuota apply when Mode == "none".
	AnonymousRPM        int `yaml:"anonymous_rpm"`
	AnonymousDailyQuota int `yaml:"anonymous_daily_quota"`
	// AnonymousFetchDailyBytes and AnonymousFetchDailyCount cap the upstream
	// traffic one anonymous client may pull per day. 0 means unlimited.
	AnonymousFetchDailyBytes int64 `yaml:"anonymous_fetch_daily_bytes"`
	AnonymousFetchDailyCount int   `yaml:"anonymous_fetch_daily_count"`
	// FetchMaxConcurrency caps how many upstream fetches one key may run at
	// the same time.
	FetchMaxConcurrency int         `yaml:"fetch_max_concurrency"`
	Keys                []KeyConfig `yaml:"keys"`
}

// KeyConfig is one API key and its limits.
type KeyConfig struct {
	Name             string   `yaml:"name"`
	Token            string   `yaml:"token"` // plaintext, or "sha256:<hex>"
	RPM              int      `yaml:"rpm"`
	Burst            int      `yaml:"burst"`
	DailyQuota       int      `yaml:"daily_quota"` // 0 = unlimited
	MaxResults       int      `yaml:"max_results"` // 0 = server default
	AllowedProviders []string `yaml:"allowed_providers"`
	// FetchDailyBytes caps the upstream bytes this key may pull per day
	// (0 = unlimited). FetchDailyCount caps the number of upstream fetches.
	FetchDailyBytes int64 `yaml:"fetch_daily_bytes"`
	FetchDailyCount int   `yaml:"fetch_daily_count"`
	// HashedToken is filled during normalization; Token is cleared.
	HashedToken string `yaml:"-"`
}

// SearchConfig tunes the search pipeline.
type SearchConfig struct {
	DefaultCount            int      `yaml:"default_count"`
	MaxCount                int      `yaml:"max_count"`
	OverallTimeout          Duration `yaml:"overall_timeout"`
	ProviderTimeout         Duration `yaml:"provider_timeout"`
	MaxConcurrency          int      `yaml:"max_concurrency"`
	NeedContentMaxResults   int      `yaml:"need_content_max_results"`
	ContentFetchTimeout     Duration `yaml:"content_fetch_timeout"`
	ContentFetchConcurrency int      `yaml:"content_fetch_concurrency"`
	PerDomainLimit          int      `yaml:"per_domain_limit"`
	CacheTTL                Duration `yaml:"cache_ttl"`
	SiteIconTemplate        string   `yaml:"site_icon_template"`
	// HedgeDelay bounds how long the pipeline keeps waiting for slow providers
	// once at least one source returned enough results.
	HedgeDelay Duration `yaml:"hedge_delay"`
}

// FetchConfig tunes the /v1/tools/fetch endpoint.
type FetchConfig struct {
	Timeout         Duration `yaml:"timeout"`
	MaxBytes        int64    `yaml:"max_bytes"`
	MaxRedirects    int      `yaml:"max_redirects"`
	AllowPrivate    bool     `yaml:"allow_private"`
	AllowPorts      []int    `yaml:"allow_ports"`
	UserAgent       string   `yaml:"user_agent"`
	CacheTTL        Duration `yaml:"cache_ttl"`
	MinContentRunes int      `yaml:"min_content_runes"`

	// MaxConcurrency caps concurrent upstream fetches across all keys.
	MaxConcurrency int `yaml:"max_concurrency"`
	// ServerDailyBytes caps the total upstream bytes pulled per day across
	// all keys (0 = unlimited).
	ServerDailyBytes int64 `yaml:"server_daily_bytes"`
	// IPVersion selects the address family used for upstream fetches:
	// "4", "6", or empty for any.
	IPVersion string `yaml:"ip_version"`
}

// ProviderConfig is one upstream search source.
type ProviderConfig struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"` // searxng|bingrss|binghtml|duckduckgo|brave|tavily
	Enabled *bool             `yaml:"enabled"`
	Weight  float64           `yaml:"weight"`
	Timeout Duration          `yaml:"timeout"`
	RPM     int               `yaml:"rpm"`
	BaseURL string            `yaml:"base_url"`
	APIKey  string            `yaml:"api_key"`
	Token   string            `yaml:"token"`
	Region  string            `yaml:"region"`
	Headers map[string]string `yaml:"headers"`

	// Browser-fingerprint overrides. Empty values fall back to built-in
	// realistic Chrome values for the Bing/DuckDuckGo scrapers.
	UserAgent string `yaml:"user_agent"`
	Referer   string `yaml:"referer"`
	Cookies   string `yaml:"cookies"` // raw Cookie header, merged with the session jar
	// Warmup controls the homepage pre-visit that establishes cookies.
	Warmup *bool `yaml:"warmup"`

	// Bing market fingerprint. Market is sent as setmkt= (e.g. "zh-CN",
	// "en-WW"); it also selects the Accept-Language header. Mobile switches to
	// a mobile UA plus the mobile search-box parameters (form=LBT003, PC=...).
	Market string `yaml:"market"`
	Mobile bool   `yaml:"mobile"`
	// ExtraParams are appended verbatim to every search URL.
	ExtraParams map[string]string `yaml:"extra_params"`

	// SourceIPPool lists source addresses to bind this provider's outgoing
	// connections to. The literal "auto" expands to the host's global
	// unicast addresses. Every distinct source address is a distinct client
	// for upstream per-IP rate limiting.
	SourceIPPool []string `yaml:"source_ip_pool"`
	// Proxy routes this provider's upstream calls through an HTTP proxy.
	Proxy string `yaml:"proxy"`
}

// IsEnabled reports whether the provider should be registered.
func (p ProviderConfig) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

// IsWarmupEnabled reports whether the homepage warm-up is enabled (default true).
func (p ProviderConfig) IsWarmupEnabled() bool { return p.Warmup == nil || *p.Warmup }

// CacheConfig sizes the response caches.
type CacheConfig struct {
	SearchMaxEntries int      `yaml:"search_max_entries"`
	FetchMaxEntries  int      `yaml:"fetch_max_entries"`
	SearchTTL        Duration `yaml:"search_ttl"`
	FetchTTL         Duration `yaml:"fetch_ttl"`
	CleanupInterval  Duration `yaml:"cleanup_interval"`
}

// LogConfig controls structured logging.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug|info|warn|error
	Format string `yaml:"format"` // json|text
}

// boolPtr returns a pointer to b, for optional booleans in YAML.
func boolPtr(b bool) *bool { return &b }

// Default returns a fully populated configuration that works without any file.
func Default() *Config {
	yes := true
	return &Config{
		Server: ServerConfig{
			Addr:              ":8080",
			ReadHeaderTimeout: Duration(5 * time.Second),
			RequestTimeout:    Duration(30 * time.Second),
			ShutdownTimeout:   Duration(10 * time.Second),
			MaxBodyBytes:      1 << 20, // 1 MiB
		},
		Auth: AuthConfig{
			Mode:                "none",
			AnonymousRPM:        60,
			AnonymousDailyQuota: 0,
			FetchMaxConcurrency: 4,
		},
		Search: SearchConfig{
			DefaultCount:            10,
			MaxCount:                50,
			OverallTimeout:          Duration(12 * time.Second),
			ProviderTimeout:         Duration(2500 * time.Millisecond),
			MaxConcurrency:          32,
			NeedContentMaxResults:   50,
			ContentFetchTimeout:     Duration(10 * time.Second),
			ContentFetchConcurrency: 16,
			PerDomainLimit:          3,
			CacheTTL:                Duration(5 * time.Minute),
			SiteIconTemplate:        "https://{host}/favicon.ico",
			HedgeDelay:              Duration(800 * time.Millisecond),
		},
		Fetch: FetchConfig{
			Timeout:         Duration(10 * time.Second),
			MaxBytes:        5 << 20, // 5 MiB
			MaxRedirects:    15,
			AllowPorts:      []int{80, 443, 8080, 8443},
			UserAgent:       "Mozilla/5.0 (compatible; OneSearchAPI/1.0; +https://github.com/Wu-jiyan/one-search-api)",
			CacheTTL:        Duration(30 * time.Minute),
			MinContentRunes: 120,
			MaxConcurrency:  32,
		},
		Providers: []ProviderConfig{
			{Name: "bing", Type: "bingrss", Enabled: &yes, Weight: 0.9, Timeout: Duration(6 * time.Second), BaseURL: "https://cn.bing.com", Market: "zh-CN"},
			{Name: "searxng", Type: "searxng", Enabled: boolPtr(false), Weight: 1.1, BaseURL: "http://127.0.0.1:8888"},
			{Name: "ddg", Type: "duckduckgo", Enabled: boolPtr(false), Weight: 0.8, BaseURL: "https://html.duckduckgo.com"},
		},
		Cache: CacheConfig{
			SearchMaxEntries: 512,
			FetchMaxEntries:  512,
			SearchTTL:        Duration(5 * time.Minute),
			FetchTTL:         Duration(30 * time.Minute),
			CleanupInterval:  Duration(time.Minute),
		},
		Log: LogConfig{Level: "info", Format: "json"},
	}
}

// Load reads path (optional), applies environment overrides, and validates.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read config %q: %w", path, err)
			}
			if err := yaml.Unmarshal(raw, cfg); err != nil {
				return nil, fmt.Errorf("parse config %q: %w", path, err)
			}
		} else if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("stat config %q: %w", path, statErr)
		} else if path != "config.yaml" {
			// An explicitly supplied path that does not exist is a hard error.
			return nil, fmt.Errorf("config file not found: %s", path)
		}
	}

	applyEnv(cfg)

	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := strings.TrimSpace(os.Getenv("ONESEARCH_ADDR")); v != "" {
		cfg.Server.Addr = v
	}
	if v := strings.TrimSpace(os.Getenv("ONESEARCH_AUTH_MODE")); v != "" {
		cfg.Auth.Mode = v
	}
	if v := strings.TrimSpace(os.Getenv("ONESEARCH_AUTH_KEYS")); v != "" {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// name:token[:rpm[:daily_quota[:max_results]]]
			fields := strings.Split(part, ":")
			if len(fields) < 2 {
				continue
			}
			kc := KeyConfig{Name: fields[0], Token: fields[1]}
			if len(fields) > 2 {
				kc.RPM = atoi(fields[2])
			}
			if len(fields) > 3 {
				kc.DailyQuota = atoi(fields[3])
			}
			if len(fields) > 4 {
				kc.MaxResults = atoi(fields[4])
			}
			cfg.Auth.Keys = append(cfg.Auth.Keys, kc)
		}
	}
	if v := strings.TrimSpace(os.Getenv("ONESEARCH_SEARXNG_URL")); v != "" {
		found := false
		for i := range cfg.Providers {
			if cfg.Providers[i].Type == "searxng" {
				cfg.Providers[i].BaseURL = v
				on := true
				cfg.Providers[i].Enabled = &on
				found = true
			}
		}
		if !found {
			on := true
			cfg.Providers = append(cfg.Providers, ProviderConfig{
				Name: "searxng", Type: "searxng", Enabled: &on, Weight: 1.1, BaseURL: v,
			})
		}
	}
	if v := strings.TrimSpace(os.Getenv("ONESEARCH_LOG_LEVEL")); v != "" {
		cfg.Log.Level = v
	}
	if v := strings.TrimSpace(os.Getenv("ONESEARCH_LOG_FORMAT")); v != "" {
		cfg.Log.Format = v
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// Normalize fills derived values and hashes plaintext keys.
func (c *Config) Normalize() error {
	c.Auth.Mode = strings.ToLower(strings.TrimSpace(c.Auth.Mode))
	if c.Auth.Mode == "" {
		c.Auth.Mode = "none"
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.MaxBodyBytes <= 0 {
		c.Server.MaxBodyBytes = 1 << 20
	}
	if c.Server.RequestTimeout.D() <= 0 {
		c.Server.RequestTimeout = Duration(30 * time.Second)
	}
	if c.Server.ReadHeaderTimeout.D() <= 0 {
		c.Server.ReadHeaderTimeout = Duration(5 * time.Second)
	}
	if c.Server.ShutdownTimeout.D() <= 0 {
		c.Server.ShutdownTimeout = Duration(10 * time.Second)
	}
	if c.Search.DefaultCount <= 0 {
		c.Search.DefaultCount = 10
	}
	if c.Search.MaxCount <= 0 {
		c.Search.MaxCount = 50
	}
	if c.Search.DefaultCount > c.Search.MaxCount {
		c.Search.DefaultCount = c.Search.MaxCount
	}
	if c.Search.OverallTimeout.D() <= 0 {
		c.Search.OverallTimeout = Duration(12 * time.Second)
	}
	if c.Search.ProviderTimeout.D() <= 0 {
		c.Search.ProviderTimeout = Duration(2500 * time.Millisecond)
	}
	if c.Search.ProviderTimeout.D() > c.Search.OverallTimeout.D() {
		c.Search.ProviderTimeout = c.Search.OverallTimeout
	}
	if c.Search.MaxConcurrency <= 0 {
		c.Search.MaxConcurrency = 32
	}
	if c.Search.NeedContentMaxResults <= 0 {
		c.Search.NeedContentMaxResults = 50
	}
	if c.Search.ContentFetchTimeout.D() <= 0 {
		c.Search.ContentFetchTimeout = Duration(10 * time.Second)
	}
	if c.Search.ContentFetchConcurrency <= 0 {
		c.Search.ContentFetchConcurrency = 16
	}
	if c.Search.PerDomainLimit <= 0 {
		c.Search.PerDomainLimit = 3
	}
	if c.Search.SiteIconTemplate == "" {
		c.Search.SiteIconTemplate = "https://{host}/favicon.ico"
	}
	if c.Search.HedgeDelay.D() <= 0 {
		c.Search.HedgeDelay = Duration(800 * time.Millisecond)
	}
	if c.Search.HedgeDelay.D() > c.Search.OverallTimeout.D() {
		c.Search.HedgeDelay = c.Search.OverallTimeout
	}
	if c.Fetch.Timeout.D() <= 0 {
		c.Fetch.Timeout = Duration(10 * time.Second)
	}
	if c.Fetch.MaxBytes <= 0 {
		c.Fetch.MaxBytes = 5 << 20
	}
	if c.Fetch.MaxRedirects <= 0 {
		c.Fetch.MaxRedirects = 15
	}
	if len(c.Fetch.AllowPorts) == 0 {
		c.Fetch.AllowPorts = []int{80, 443, 8080, 8443}
	}
	if c.Fetch.UserAgent == "" {
		c.Fetch.UserAgent = Default().Fetch.UserAgent
	}
	if c.Fetch.CacheTTL.D() <= 0 {
		c.Fetch.CacheTTL = Duration(30 * time.Minute)
	}
	if c.Fetch.MinContentRunes <= 0 {
		c.Fetch.MinContentRunes = 120
	}
	if c.Fetch.MaxConcurrency <= 0 {
		c.Fetch.MaxConcurrency = 32
	}
	if c.Fetch.ServerDailyBytes < 0 {
		c.Fetch.ServerDailyBytes = 0
	}
	c.Fetch.IPVersion = strings.ToLower(strings.TrimSpace(c.Fetch.IPVersion))
	switch c.Fetch.IPVersion {
	case "", "4", "6":
	default:
		return fmt.Errorf("fetch.ip_version must be 4, 6 or empty, got %q", c.Fetch.IPVersion)
	}
	if c.Cache.SearchMaxEntries <= 0 {
		c.Cache.SearchMaxEntries = 512
	}
	if c.Cache.FetchMaxEntries <= 0 {
		c.Cache.FetchMaxEntries = 512
	}
	if c.Cache.SearchTTL.D() <= 0 {
		c.Cache.SearchTTL = c.Search.CacheTTL
	}
	if c.Cache.FetchTTL.D() <= 0 {
		c.Cache.FetchTTL = c.Fetch.CacheTTL
	}
	if c.Cache.CleanupInterval.D() <= 0 {
		c.Cache.CleanupInterval = Duration(time.Minute)
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "json"
	}

	// Provider normalization.
	seen := map[string]bool{}
	for i := range c.Providers {
		p := &c.Providers[i]
		p.Type = strings.ToLower(strings.TrimSpace(p.Type))
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = p.Type
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true
		if p.Weight <= 0 {
			p.Weight = 1.0
		}
		if p.Timeout.D() <= 0 {
			p.Timeout = c.Search.ProviderTimeout
		}
		if p.BaseURL != "" {
			p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
			if u, err := url.Parse(p.BaseURL); err != nil || u.Host == "" {
				return fmt.Errorf("provider %q: invalid base_url %q", p.Name, p.BaseURL)
			}
		}
		p.UserAgent = strings.TrimSpace(p.UserAgent)
		p.Cookies = strings.TrimSpace(p.Cookies)
		p.Market = strings.TrimSpace(p.Market)
		if len(p.Market) > 32 {
			return fmt.Errorf("provider %q: market too long", p.Name)
		}
		extra := make(map[string]string, len(p.ExtraParams))
		for k, v := range p.ExtraParams {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			extra[k] = strings.TrimSpace(v)
		}
		if len(extra) > 0 {
			p.ExtraParams = extra
		} else {
			p.ExtraParams = nil
		}
		if p.Referer != "" {
			p.Referer = strings.TrimSpace(p.Referer)
			if u, err := url.Parse(p.Referer); err != nil || u.Host == "" {
				return fmt.Errorf("provider %q: invalid referer %q", p.Name, p.Referer)
			}
		}
		p.Proxy = strings.TrimSpace(p.Proxy)
		if p.Proxy != "" {
			if u, err := url.Parse(p.Proxy); err != nil || u.Host == "" {
				return fmt.Errorf("provider %q: invalid proxy %q", p.Name, p.Proxy)
			}
		}
		pool := make([]string, 0, len(p.SourceIPPool))
		seenIP := map[string]bool{}
		for _, raw := range p.SourceIPPool {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if strings.EqualFold(raw, "auto") {
				if !seenIP["auto"] {
					seenIP["auto"] = true
					pool = append(pool, "auto")
				}
				continue
			}
			ip := net.ParseIP(raw)
			if ip == nil {
				return fmt.Errorf("provider %q: invalid source_ip_pool entry %q", p.Name, raw)
			}
			v := ip.String()
			if seenIP[v] {
				continue
			}
			seenIP[v] = true
			pool = append(pool, v)
		}
		p.SourceIPPool = pool
	}

	// Key normalization: hash tokens, apply defaults.
	seenKeys := map[string]bool{}
	for i := range c.Auth.Keys {
		k := &c.Auth.Keys[i]
		k.Name = strings.TrimSpace(k.Name)
		if k.Name == "" {
			return fmt.Errorf("auth.keys[%d]: name is required", i)
		}
		if seenKeys[k.Name] {
			return fmt.Errorf("duplicate key name %q", k.Name)
		}
		seenKeys[k.Name] = true
		if strings.TrimSpace(k.Token) == "" {
			return fmt.Errorf("auth.keys[%d] (%s): token is required", i, k.Name)
		}
		k.HashedToken = HashToken(k.Token)
		k.Token = ""
		if k.RPM <= 0 {
			k.RPM = 60
		}
		if k.Burst <= 0 {
			k.Burst = max(1, k.RPM/10)
			if k.Burst > 60 {
				k.Burst = 60
			}
		}
		if k.MaxResults <= 0 {
			k.MaxResults = c.Search.MaxCount
		}
		if k.MaxResults > c.Search.MaxCount {
			k.MaxResults = c.Search.MaxCount
		}
	}
	if c.Auth.AnonymousRPM <= 0 {
		c.Auth.AnonymousRPM = 60
	}
	if c.Auth.FetchMaxConcurrency <= 0 {
		c.Auth.FetchMaxConcurrency = 4
	}
	if c.Auth.AnonymousFetchDailyBytes < 0 {
		c.Auth.AnonymousFetchDailyBytes = 0
	}
	if c.Auth.AnonymousFetchDailyCount < 0 {
		c.Auth.AnonymousFetchDailyCount = 0
	}
	for i := range c.Auth.Keys {
		k := &c.Auth.Keys[i]
		if k.FetchDailyBytes < 0 {
			k.FetchDailyBytes = 0
		}
		if k.FetchDailyCount < 0 {
			k.FetchDailyCount = 0
		}
	}
	return nil
}

// HashToken returns "sha256:<hex>" for a plaintext token. If the input is
// already in that form it is returned unchanged (after lowercasing the hex).
func HashToken(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "sha256:") {
		return "sha256:" + strings.ToLower(strings.TrimPrefix(strings.ToLower(token), "sha256:"))
	}
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Validate reports the first configuration problem.
func (c *Config) Validate() error {
	switch c.Auth.Mode {
	case "none", "key":
	default:
		return fmt.Errorf("auth.mode must be \"none\" or \"key\", got %q", c.Auth.Mode)
	}
	if c.Auth.Mode == "key" && len(c.Auth.Keys) == 0 {
		return fmt.Errorf("auth.mode is \"key\" but no auth.keys are configured")
	}
	if c.Fetch.MaxBytes > 64<<20 {
		return fmt.Errorf("fetch.max_bytes must be <= 64MiB")
	}
	if c.Server.MaxBodyBytes > 16<<20 {
		return fmt.Errorf("server.max_body_bytes must be <= 16MiB")
	}
	if c.Auth.FetchMaxConcurrency < 0 {
		return fmt.Errorf("auth.fetch_max_concurrency must be >= 0")
	}
	for _, port := range c.Fetch.AllowPorts {
		if port <= 0 || port > 65535 {
			return fmt.Errorf("fetch.allow_ports: invalid port %d", port)
		}
	}
	enabled := 0
	for _, p := range c.Providers {
		switch p.Type {
		case "searxng", "bingrss", "binghtml", "duckduckgo", "brave", "tavily":
		default:
			return fmt.Errorf("provider %q: unknown type %q", p.Name, p.Type)
		}
		if !p.IsEnabled() {
			continue
		}
		enabled++
		switch p.Type {
		case "searxng", "bingrss", "binghtml", "duckduckgo":
			if p.BaseURL == "" {
				return fmt.Errorf("provider %q (%s): base_url is required", p.Name, p.Type)
			}
		case "brave", "tavily":
			if p.APIKey == "" {
				return fmt.Errorf("provider %q (%s): api_key is required", p.Name, p.Type)
			}
		}
	}
	if enabled == 0 {
		return fmt.Errorf("no enabled search providers")
	}
	return nil
}

// KeyConfigByName finds a key configuration by name.
func (c *Config) KeyConfigByName(name string) (KeyConfig, bool) {
	for _, k := range c.Auth.Keys {
		if k.Name == name {
			return k, true
		}
	}
	return KeyConfig{}, false
}
