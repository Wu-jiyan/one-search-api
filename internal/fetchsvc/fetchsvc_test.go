package fetchsvc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/httpx"
	"github.com/wujiyan/one-search-api/internal/obs"
)

const testPage = `<!DOCTYPE html><html><head><title>测试页面</title>
<meta property="og:site_name" content="测试站点"></head><body><article>
<h1>测试页面标题</h1>
<p>这是正文第一段，内容足够长以便通过可读性阈值和最小正文长度检查。</p>
<p>这是正文第二段，继续补充更多文字内容，确保抽取结果稳定可靠。</p>
<p>这是正文第三段，再次增加文字量，避免被判定为无正文页面。</p>
</article></body></html>`

// testConfig builds a config whose guard allows the given httptest servers.
func testConfig(t *testing.T, timeout time.Duration, servers ...string) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Fetch.AllowPrivate = true
	cfg.Fetch.MinContentRunes = 40
	cfg.Fetch.MaxBytes = 1 << 20
	cfg.Fetch.Timeout = config.Duration(timeout)
	for _, raw := range servers {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse server url %q: %v", raw, err)
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			t.Fatalf("server url %q has no port: %v", raw, err)
		}
		cfg.Fetch.AllowPorts = append(cfg.Fetch.AllowPorts, port)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return cfg
}

func TestFetchExtractsMarkdown(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	svc := New(testConfig(t, 5*time.Second, srv.URL), obs.NewMetrics())
	resp, err := svc.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(resp.Title, "测试页面") {
		t.Errorf("Title = %q", resp.Title)
	}
	if !strings.Contains(resp.Content, "正文第一段") {
		t.Errorf("Content = %q", resp.Content)
	}

	// Second call must be served from cache.
	if _, err := svc.Fetch(context.Background(), srv.URL); err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("upstream hits = %d, want 1 (cache)", got)
	}
}

func TestFetchBlocksSSRF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	cfg := testConfig(t, 5*time.Second, srv.URL)
	cfg.Fetch.AllowPrivate = false
	svc := New(cfg, obs.NewMetrics())

	_, err := svc.Fetch(context.Background(), srv.URL)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err = %v, want 403 APIError", err)
	}
}

func TestFetchUpstreamErrorBecomesNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := New(testConfig(t, 5*time.Second, srv.URL), obs.NewMetrics())
	_, err := svc.Fetch(context.Background(), srv.URL)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want 404", err)
	}
}

func TestFetchUnsupportedContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	}))
	defer srv.Close()

	svc := New(testConfig(t, 5*time.Second, srv.URL), obs.NewMetrics())
	_, err := svc.Fetch(context.Background(), srv.URL)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want 404 for unsupported type", err)
	}
}

func TestFetchTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()

	cfg := testConfig(t, 5*time.Second, srv.URL)
	cfg.Fetch.MaxBytes = 256
	svc := New(cfg, obs.NewMetrics())

	_, err := svc.Fetch(context.Background(), srv.URL)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("err = %v, want 413", err)
	}
}

func TestFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	svc := New(testConfig(t, 60*time.Millisecond, srv.URL), obs.NewMetrics())
	_, err := svc.Fetch(context.Background(), srv.URL)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusGatewayTimeout {
		t.Fatalf("err = %v, want 504", err)
	}
}

func TestFetchNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body></body></html>"))
	}))
	defer srv.Close()

	svc := New(testConfig(t, 5*time.Second, srv.URL), obs.NewMetrics())
	_, err := svc.Fetch(context.Background(), srv.URL)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want 404", err)
	}
}

func TestValidateURLRejectsBadScheme(t *testing.T) {
	svc := New(testConfig(t, time.Second), obs.NewMetrics())
	if _, err := svc.ValidateURL("file:///etc/passwd"); err == nil {
		t.Fatal("expected validation error")
	}
}
