// Package provider defines the search-source interface and the supported
// upstream implementations (SearXNG, Bing RSS, DuckDuckGo, Brave, Tavily).
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/query"
)

// Result is one raw search hit produced by a provider, before enrichment.
type Result struct {
	Title    string
	URL      string
	Snippet  string
	Date     time.Time
	Engine   string
	Rank     int
	RawScore float64
	SiteName string
}

// Provider is one upstream search source.
type Provider interface {
	Name() string
	Type() string
	Search(ctx context.Context, q query.Query) ([]Result, error)
}

// New builds a provider from configuration.
func New(cfg config.ProviderConfig, userAgent string) (Provider, error) {
	client := NewHTTPClient(cfg.Timeout.D(), userAgent)

	switch cfg.Type {
	case "searxng":
		return &searxng{name: cfg.Name, base: cfg.BaseURL, token: cfg.Token,
			headers: cfg.Headers, region: cfg.Region, client: client}, nil
	case "bingrss":
		return &bingRSS{name: cfg.Name, base: cfg.BaseURL, headers: cfg.Headers,
			region: cfg.Region, client: client}, nil
	case "duckduckgo":
		return &duckDuckGo{name: cfg.Name, base: cfg.BaseURL, headers: cfg.Headers,
			region: cfg.Region, client: client}, nil
	case "brave":
		return &brave{name: cfg.Name, apiKey: cfg.APIKey, base: baseOr(cfg.BaseURL, "https://api.search.brave.com"),
			headers: cfg.Headers, region: cfg.Region, client: client}, nil
	case "tavily":
		return &tavily{name: cfg.Name, apiKey: cfg.APIKey, base: baseOr(cfg.BaseURL, "https://api.tavily.com"),
			headers: cfg.Headers, region: cfg.Region, client: client}, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", cfg.Type)
	}
}

func baseOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimRight(v, "/")
}

// NewHTTPClient returns a client tuned for metered upstream calls.
func NewHTTPClient(timeout time.Duration, userAgent string) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if userAgent == "" {
		userAgent = "OneSearchAPI/1.0"
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// UserAgentOf extracts the configured user agent, falling back sensibly.
func UserAgentOf(cfg *config.Config) string {
	if cfg != nil && cfg.Fetch.UserAgent != "" {
		return cfg.Fetch.UserAgent
	}
	return "OneSearchAPI/1.0"
}

func nowUTC() time.Time { return time.Now().UTC() }

// doJSON performs a request and decodes a JSON body into dst.
func doJSON(ctx context.Context, client *http.Client, req *http.Request, dst any) error {
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Body: truncate(string(body), 300)}
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

// HTTPError is a non-200 response from an upstream.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("upstream status %d: %s", e.Status, e.Body)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

// cleanText strips tags, unescapes entities and collapses whitespace.
func cleanText(s string) string {
	if s == "" {
		return ""
	}
	s = stripTags(s)
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// ParseDate tries the date formats commonly returned by search sources.
func ParseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02Z07:00",
		"2006-01-02",
		"2006/01/02",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		"Mon, 02 Jan 2006 15:04:05 GMT",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"02 Jan 2006 15:04:05 -0700",
		"Jan 2, 2006",
		"2 Jan 2006",
		"2006年01月02日",
		"2006年1月2日",
		"2006年01月02日 15:04:05",
		"2006年1月2日 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	if t, ok := parseLooseDate(s); ok {
		return t
	}
	// Unix timestamps (seconds or milliseconds).
	if allDigits(s) {
		if len(s) >= 13 {
			if ms, err := parseInt64(s); err == nil {
				return time.UnixMilli(ms).UTC()
			}
		}
		if sec, err := parseInt64(s); err == nil && sec > 1_000_000_000 {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Time{}
}

// reDateDMY matches localized day-month-year strings such as
// "周六, 17 10月 2026 12:42:00 GMT" or "Sat, 17 Oct 2026 12:42:00 GMT".
var reDateDMY = regexp.MustCompile(`(?i)^\s*(?:[^\d\s]{1,4}[,.]?\s*)?(\d{1,2})\s+([^\s,]{1,8})\s+(\d{4})(?:[ ,T]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)

// reDateMDY matches "Oct 17, 2026" / "October 17 2026" style strings.
var reDateMDY = regexp.MustCompile(`(?i)^\s*(?:[^\d\s]{1,4}[,.]?\s*)?([A-Za-z]{3,9})\.?\s+(\d{1,2}),?\s+(\d{4})(?:[ ,T]+(\d{1,2}):(\d{2})(?::(\d{2}))?)?`)

// parseLooseDate handles localized RSS/HTML date strings that the fixed
// layouts cannot express (notably Chinese Bing results).
func parseLooseDate(s string) (time.Time, bool) {
	if m := reDateDMY.FindStringSubmatch(s); m != nil {
		mon, ok := monthFromToken(m[2])
		if !ok {
			return time.Time{}, false
		}
		day, _ := strconv.Atoi(m[1])
		year, _ := strconv.Atoi(m[3])
		t := buildTime(year, mon, day, m[4], m[5], m[6])
		if !t.IsZero() {
			return t, true
		}
	}
	if m := reDateMDY.FindStringSubmatch(s); m != nil {
		mon, ok := monthFromToken(m[1])
		if !ok {
			return time.Time{}, false
		}
		day, _ := strconv.Atoi(m[2])
		year, _ := strconv.Atoi(m[3])
		t := buildTime(year, mon, day, m[4], m[5], m[6])
		if !t.IsZero() {
			return t, true
		}
	}
	return time.Time{}, false
}

func buildTime(year int, mon time.Month, day int, hh, mm, ss string) time.Time {
	if year < 1900 || year > 2200 || day < 1 || day > 31 {
		return time.Time{}
	}
	hour, minute, sec := 0, 0, 0
	if hh != "" {
		hour, _ = strconv.Atoi(hh)
	}
	if mm != "" {
		minute, _ = strconv.Atoi(mm)
	}
	if ss != "" {
		sec, _ = strconv.Atoi(ss)
	}
	return time.Date(year, mon, day, hour, minute, sec, 0, time.UTC)
}

var zhMonths = map[string]time.Month{
	"一": 1, "二": 2, "三": 3, "四": 4, "五": 5, "六": 6,
	"七": 7, "八": 8, "九": 9, "十": 10, "十一": 11, "十二": 12,
}

var enMonths = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}

func monthFromToken(tok string) (time.Month, bool) {
	tok = strings.TrimSpace(strings.Trim(tok, ".,"))
	tok = strings.TrimSuffix(tok, "月")
	if m, ok := zhMonths[tok]; ok {
		return m, true
	}
	if n, err := strconv.Atoi(tok); err == nil && n >= 1 && n <= 12 {
		return time.Month(n), true
	}
	lower := strings.ToLower(tok)
	for i, name := range enMonths {
		if strings.HasPrefix(lower, name) {
			return time.Month(i + 1), true
		}
	}
	return 0, false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseInt64(s string) (int64, error) {
	var n int64
	for _, r := range s {
		n = n*10 + int64(r-'0')
		if n < 0 {
			return 0, fmt.Errorf("overflow")
		}
	}
	return n, nil
}

// NormalizeURL validates and canonicalizes a result URL.
func NormalizeURL(raw string) (*url.URL, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ""
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, ""
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)

	q := u.Query()
	changed := false
	for k := range q {
		if isTrackingParam(k) {
			q.Del(k)
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u, u.String()
}

func isTrackingParam(key string) bool {
	lk := strings.ToLower(key)
	return strings.HasPrefix(lk, "utm_") || lk == "fbclid" || lk == "gclid" ||
		lk == "spm" || lk == "ref" || lk == "ref_src" || lk == "share_token" ||
		lk == "ncid" || lk == "ocid" || lk == "cmpid"
}

// HostKey is the dedupe-friendly host+path+query key.
func HostKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	q := u.Query()
	for k := range q {
		if isTrackingParam(k) {
			q.Del(k)
		}
	}
	return strings.ToLower(u.Host) + strings.TrimSuffix(u.Path, "/") + "?" + q.Encode()
}
