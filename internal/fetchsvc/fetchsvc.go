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

// maxContentRunes bounds the Markdown returned to clients.
const maxContentRunes = 200_000

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
func MapError(err error) *httpx.APIError {
	if err == nil {
		return nil
	}
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, fetchguard.ErrBlocked):
		return httpx.Forbidden("url is not allowed: " + err.Error())
	case errors.Is(err, fetchguard.ErrInvalid):
		return httpx.InvalidRequest(err.Error())
	default:
		return httpx.InvalidRequest(err.Error())
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
		return nil, httpx.NotFound("unable to fetch url: upstream returned status " + itoa(resp.StatusCode))
	}
	ct := resp.Header.Get("Content-Type")
	if !allowedContentType(ct) {
		s.metrics.Inc("onesearch_fetch_total", "status", "unsupported_type")
		return nil, httpx.NotFound("unable to fetch url: unsupported content type " + ct)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, s.cfg.MaxBytes+1))
	if err != nil {
		if isTimeout(err) {
			return nil, httpx.UpstreamTimeout("timed out while reading the page")
		}
		return nil, httpx.NotFound("unable to read page body")
	}
	if int64(len(body)) > s.cfg.MaxBytes {
		s.metrics.Inc("onesearch_fetch_total", "status", "too_large")
		e := httpx.InvalidRequest("page exceeds the maximum allowed size")
		e.Status = http.StatusRequestEntityTooLarge
		e.Code = "payload_too_large"
		return nil, e
	}

	page, err := extract.Extract(u.String(), body, ct)
	if err != nil {
		s.metrics.Inc("onesearch_fetch_total", "status", "no_content")
		return nil, httpx.NotFound("unable to extract page content")
	}
	if utf8.RuneCountInString(page.Text) < s.cfg.MinContentRunes {
		s.metrics.Inc("onesearch_fetch_total", "status", "no_content")
		return nil, httpx.NotFound("unable to extract page content")
	}
	s.metrics.Inc("onesearch_fetch_total", "status", "ok")
	return &page, nil
}

func allowedContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return true
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "text/html", "application/xhtml+xml", "text/plain", "application/xml", "text/xml":
		return true
	}
	return false
}

func mapGuardError(err error) error {
	switch {
	case errors.Is(err, fetchguard.ErrBlocked):
		return httpx.Forbidden("url is not allowed: " + err.Error())
	case errors.Is(err, fetchguard.ErrInvalid):
		return httpx.InvalidRequest(err.Error())
	default:
		return httpx.InvalidRequest(err.Error())
	}
}

func mapNetError(err error) error {
	if errors.Is(err, fetchguard.ErrBlocked) {
		return httpx.Forbidden("url is blocked by security policy")
	}
	if errors.Is(err, fetchguard.ErrInvalid) {
		return httpx.InvalidRequest(err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return httpx.UpstreamTimeout("fetch timed out")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return httpx.UpstreamTimeout("fetch timed out")
	}
	return httpx.NotFound("unable to fetch url: " + err.Error())
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
