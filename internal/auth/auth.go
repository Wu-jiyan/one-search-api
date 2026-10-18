// Package auth implements API-key authentication plus per-key RPM and daily
// quota accounting. All state is in memory and resets on restart.
package auth

import (
	"crypto/subtle"
	"sync"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/httpx"
	"github.com/Wu-jiyan/one-search-api/internal/limit"
)

// Key is a configured API key with its live quota state.
type Key struct {
	Name       string
	Hashed     string
	DailyQuota int
	MaxResults int
	Allowed    map[string]bool

	bucket *limit.Bucket

	mu   sync.Mutex
	day  string
	used int

	lastUse time.Time
}

// AllowedProvider reports whether this key may use the named provider.
func (k *Key) AllowedProvider(name string) bool {
	if len(k.Allowed) == 0 {
		return true
	}
	return k.Allowed[name]
}

// AllowedSet returns the provider allowlist, or nil for "all".
func (k *Key) AllowedSet() map[string]bool { return k.Allowed }

// DailyUsed returns the number of charged requests today.
func (k *Key) DailyUsed() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.rollLocked()
	return k.used
}

// Admit applies RPM and daily quota checks, then charges one request.
func (k *Key) Admit() *httpx.APIError {
	k.mu.Lock()
	defer k.mu.Unlock()

	if ok, wait := k.bucket.Allow(); !ok {
		return httpx.RateLimited("rate limit exceeded: too many requests per minute", wait)
	}
	k.rollLocked()
	if k.DailyQuota > 0 && k.used >= k.DailyQuota {
		return httpx.QuotaExceeded("daily quota exceeded for this api key")
	}
	k.used++
	return nil
}

// CheckRPM applies only the RPM limit (used by endpoints that do not consume
// daily quota, e.g. fetch).
func (k *Key) CheckRPM() *httpx.APIError {
	if ok, wait := k.bucket.Allow(); !ok {
		return httpx.RateLimited("rate limit exceeded: too many requests per minute", wait)
	}
	return nil
}

func (k *Key) rollLocked() {
	today := time.Now().Format("2006-01-02")
	if k.day != today {
		k.day = today
		k.used = 0
	}
}

// Authenticator resolves Bearer tokens to keys and owns the anonymous
// per-client registry used when auth.mode is "none".
type Authenticator struct {
	mode   string
	keys   []*Key
	anon   *config.AuthConfig
	mu     sync.Mutex
	byIP   map[string]*Key
	lastGC time.Time
}

// New builds an Authenticator from configuration.
func New(cfg config.AuthConfig) *Authenticator {
	a := &Authenticator{mode: cfg.Mode, anon: &cfg, byIP: map[string]*Key{}}
	for _, kc := range cfg.Keys {
		k := &Key{
			Name:       kc.Name,
			Hashed:     kc.HashedToken,
			DailyQuota: kc.DailyQuota,
			MaxResults: kc.MaxResults,
			bucket:     limit.NewBucket(kc.RPM, kc.Burst),
			day:        time.Now().Format("2006-01-02"),
		}
		if len(kc.AllowedProviders) > 0 {
			k.Allowed = make(map[string]bool, len(kc.AllowedProviders))
			for _, p := range kc.AllowedProviders {
				k.Allowed[p] = true
			}
		}
		a.keys = append(a.keys, k)
	}
	return a
}

// Mode returns "key" or "none".
func (a *Authenticator) Mode() string { return a.mode }

// Authenticate resolves a Bearer token. In "none" mode every token is accepted
// and mapped to the anonymous key for the given client IP.
func (a *Authenticator) Authenticate(token, clientIP string) (*Key, *httpx.APIError) {
	if a.mode == "none" {
		return a.anonymous(clientIP), nil
	}
	if token == "" {
		return nil, httpx.Unauthorized("Authentication Fails (missing key)")
	}
	if len(a.keys) == 0 {
		return nil, httpx.Unauthorized("Authentication Fails (no api keys configured)")
	}
	hashed := config.HashToken(token)
	for _, k := range a.keys {
		if subtle.ConstantTimeCompare([]byte(hashed), []byte(k.Hashed)) == 1 {
			return k, nil
		}
	}
	// Match the reference API's wording: only the tail of the key is echoed.
	tail := token
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return nil, httpx.Unauthorized("Authentication Fails, Your api key: ****" + tail + " is invalid")
}

// anonymous returns a client-specific synthetic key.
func (a *Authenticator) anonymous(ip string) *Key {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	if now.Sub(a.lastGC) > 10*time.Minute {
		for addr, k := range a.byIP {
			if now.Sub(k.bucketLastUse()) > 10*time.Minute {
				delete(a.byIP, addr)
			}
		}
		a.lastGC = now
	}
	if k, ok := a.byIP[ip]; ok {
		k.touch()
		return k
	}
	k := &Key{
		Name:       "anon:" + ip,
		DailyQuota: a.anon.AnonymousDailyQuota,
		MaxResults: 0,
		bucket:     limit.NewBucket(a.anon.AnonymousRPM, 0),
		day:        now.Format("2006-01-02"),
	}
	k.touch()
	if len(a.byIP) > 10000 {
		// Bound memory under abuse: drop an arbitrary entry.
		for addr := range a.byIP {
			delete(a.byIP, addr)
			break
		}
	}
	a.byIP[ip] = k
	return k
}

func (k *Key) touch() {
	k.mu.Lock()
	k.lastUse = time.Now()
	k.mu.Unlock()
}

func (k *Key) bucketLastUse() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.lastUse
}
