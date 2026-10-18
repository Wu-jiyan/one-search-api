// Package fetchsvc implements the /v1/tools/fetch pipeline: URL validation,
// SSRF-guarded HTTP fetch, readability extraction, caching and error mapping.
package fetchsvc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wujiyan/one-search-api/internal/cache"
	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/extract"
	"github.com/wujiyan/one-search-api/internal/fetchguard"
	"github.com/wujiyan/one-search-api/internal/httpx"
	"github.com/wujiyan/one-search-api/internal/obs"
)

// maxContentRunes bounds the Markdown returned to clients. The reference API
// returned a 447k-rune RFC document intact, so this cap sits comfortably above
// it while still bounding pathological pages.
const maxContentRunes = 500_000

// Service fetches and extracts single web pages.
type Service struct {
	cfg       config.FetchConfig
	guard     *fetchguard.Guard
	client    *http.Client
	cache     *cache.Cache[*extract.Page]
	flight    *cache.Group[*extract.Page]
	metrics   *obs.Metrics
	userAgent string
}

// New builds a fetch service.
func New(cfg *config.Config, metrics *obs.Metrics) *Service {
	guard := fetchguard.New(cfg.Fetch.AllowPrivate, cfg.Fetch.AllowPorts, cfg.Fetch.MaxRedirects)
	return &Service{
		cfg:       cfg.Fetch,
		guard:     guard,
		client:    guard.NewClient(cfg.Fetch.UserAgent, cfg.Fetch.Timeout.D(), cfg.Fetch.MaxRedirects),
		cache:     cache.New[*extract.Page](cfg.Cache.FetchMaxEntries, cfg.Cache.FetchTTL.D()),
		flight:    &cache.Group[*extract.Page]{},
		metrics:   metrics,
		userAgent: cfg.Fetch.UserAgent,
	}
}

// ValidateURL exposes the guard check for the API layer.
func (s *Service) ValidateURL(raw string) (*url.URL, error) {
	return s.guard.ValidateURL(raw)
}

// MapError converts a fetchguard error into the public API error contract.
// Messages mirror the reference API's validation wording.
func MapError(err error) *httpx.APIError {
	if err == nil {
		return nil
	}
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, fetchguard.ErrCredentials):
		return httpx.InvalidRequest("'url' must not contain credentials")
	case errors.Is(err, fetchguard.ErrURLTooLong):
		return httpx.InvalidRequest("'url' must be at most 8192 characters")
	case errors.Is(err, fetchguard.ErrBadScheme), errors.Is(err, fetchguard.ErrNoHost):
		return httpx.InvalidRequest("'url' must be an HTTP or HTTPS URL with a host")
	case errors.Is(err, fetchguard.ErrBlocked):
		// The reference API reports private/blocked targets as an opaque 404
		// rather than exposing its policy; match that so clients behave the same.
		return httpx.NotFound("failed to fetch URL")
	case errors.Is(err, fetchguard.ErrInvalid):
		return httpx.InvalidRequest("'url' must be a valid HTTP or HTTPS URL")
	default:
		return httpx.InvalidRequest("'url' must be a valid HTTP or HTTPS URL")
	}
}

// Fetch returns title and Markdown content for a URL.
func (s *Service) Fetch(ctx context.Context, rawURL string) (*httpx.FetchResponse, error) {
	page, err := s.Page(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	content := page.Content
	if strings.TrimSpace(content) == "" {
		content = page.Text
	}
	return &httpx.FetchResponse{
		Title:   page.Title,
		Content: truncateRunes(content, maxContentRunes),
	}, nil
}

// Page fetches and caches the extracted page.
func (s *Service) Page(ctx context.Context, rawURL string) (*extract.Page, error) {
	u, err := s.guard.ValidateURL(rawURL)
	if err != nil {
		return nil, mapGuardError(err)
	}
	key := cacheKey(u)

	if p, ok := s.cache.Get(key); ok {
		s.metrics.Inc("onesearch_cache_hits_total", "kind", "fetch")
		return p, nil
	}

	page, err := cache.Do(s.flight, key, func() (*extract.Page, error) {
		p, ferr := s.fetchOnce(ctx, u)
		if ferr != nil {
			return nil, ferr
		}
		s.cache.Set(key, p)
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

func (s *Service) fetchOnce(ctx context.Context, u *url.URL) (*extract.Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, httpx.InvalidRequest("invalid url: " + err.Error())
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.1")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	start := time.Now()
	resp, err := s.client.Do(req)
	s.metrics.ObserveDuration("onesearch_fetch_duration_seconds", time.Since(start))
	if err != nil {
		return nil, mapNetError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.metrics.Inc("onesearch_fetch_total", "status", "upstream_error")
		return nil, httpx.NotFound("failed to fetch URL")
	}
	ct := resp.Header.Get("Content-Type")
	if !allowedContentType(ct) {
		s.metrics.Inc("onesearch_fetch_total", "status", "unsupported_type")
		return nil, httpx.NotFound("failed to fetch URL")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, s.cfg.MaxBytes+1))
	if err != nil {
		if isTimeout(err) {
			return nil, httpx.UpstreamTimeout("Request timeout")
		}
		return nil, httpx.NotFound("failed to fetch URL")
	}
	if int64(len(body)) > s.cfg.MaxBytes {
		s.metrics.Inc("onesearch_fetch_total", "status", "too_large")
		return nil, httpx.PayloadTooLarge("page exceeds the maximum allowed size")
	}

	page, err := extract.Extract(u.String(), body, ct)
	if err != nil {
		s.metrics.Inc("onesearch_fetch_total", "status", "no_content")
		return nil, httpx.NotFound("failed to fetch URL")
	}
	// The reference API only enforces a minimum body length for HTML pages;
	// short plain-text resources (robots.txt, RFCs, source files) still succeed.
	if isHTMLType(ct) && utf8.RuneCountInString(page.Text) < s.cfg.MinContentRunes {
		s.metrics.Inc("onesearch_fetch_total", "status", "no_content")
		return nil, httpx.NotFound("failed to fetch URL")
	}
	if strings.TrimSpace(page.Text) == "" {
		s.metrics.Inc("onesearch_fetch_total", "status", "no_content")
		return nil, httpx.NotFound("failed to fetch URL")
	}
	s.metrics.Inc("onesearch_fetch_total", "status", "ok")
	return &page, nil
}

// baseContentType lowercases the media type and drops any parameters.
func baseContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// allowedContentType mirrors the reference API's measured media-type policy:
// text-like documents and JSON pass through, while XML, CSV, PDF and binary
// payloads are reported as 404. Accepting a few extra text types is harmless.
func allowedContentType(ct string) bool {
	mt := baseContentType(ct)
	if mt == "" {
		return true // no header: let the extractor sniff
	}
	switch mt {
	case "text/html", "application/xhtml+xml", "text/xhtml",
		"text/plain", "text/markdown", "text/css", "text/javascript",
		"application/json", "application/javascript", "text/richtext":
		return true
	}
	if strings.HasPrefix(mt, "text/") {
		// text/xml and text/csv are the two text types the reference API rejects.
		return mt != "text/xml" && mt != "text/csv"
	}
	return false
}

func isHTMLType(ct string) bool {
	mt := baseContentType(ct)
	return mt == "" || mt == "text/html" || mt == "application/xhtml+xml" || mt == "text/xhtml"
}

func mapGuardError(err error) error {
	if apiErr := MapError(err); apiErr != nil {
		return apiErr
	}
	return httpx.InvalidRequest("'url' must be a valid HTTP or HTTPS URL")
}

func mapNetError(err error) error {
	if errors.Is(err, fetchguard.ErrInvalid) {
		if apiErr := MapError(err); apiErr != nil {
			return apiErr
		}
		return httpx.InvalidRequest("'url' must be a valid HTTP or HTTPS URL")
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return httpx.UpstreamTimeout("Request timeout")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return httpx.UpstreamTimeout("Request timeout")
	}
	// Blocked destinations, DNS failures and refused connections are all opaque
	// 404s in the reference API.
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return httpx.NotFound("failed to fetch URL")
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func cacheKey(u *url.URL) string {
	host := strings.ToLower(u.Host)
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || lk == "fbclid" || lk == "gclid" || lk == "spm" || lk == "ref" {
			q.Del(k)
		}
	}
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" {
		path = "/"
	}
	key := u.Scheme + "://" + host + path
	if enc := q.Encode(); enc != "" {
		key += "?" + enc
	}
	return key
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "\n\n[content truncated]"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
