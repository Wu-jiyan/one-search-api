package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/query"
)

func TestParseDate(t *testing.T) {
	cases := map[string]bool{
		"2026-10-01T08:00:00+08:00":     true,
		"2026-10-01T00:00:00Z":          true,
		"Sat, 17 Oct 2026 12:42:00 GMT": true,
		"2026-10-01":                    true,
		"2026/10/01":                    true,
		"2026年10月01日":                   true,
		"1790000000":                    true,
		"not a date":                    false,
		"":                              false,
	}
	for raw, wantOK := range cases {
		got := ParseDate(raw)
		if wantOK && got.IsZero() {
			t.Errorf("ParseDate(%q) = zero, want a time", raw)
		}
		if !wantOK && !got.IsZero() {
			t.Errorf("ParseDate(%q) = %v, want zero", raw, got)
		}
	}
}

func TestParseDateLocalized(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Time
	}{
		{"周六, 17 10月 2026 12:42:00 GMT", time.Date(2026, 10, 17, 12, 42, 0, 0, time.UTC)},
		{"17 10月 2026 08:00:00 GMT", time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)},
		{"Sat, 17 Oct 2026 12:42:00 GMT", time.Date(2026, 10, 17, 12, 42, 0, 0, time.UTC)},
		{"Oct 17, 2026", time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)},
		{"17 October 2026", time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)},
		{"2026年10月17日 12:42:00", time.Date(2026, 10, 17, 12, 42, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		got := ParseDate(tc.raw)
		if !got.Equal(tc.want) {
			t.Errorf("ParseDate(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestNormalizeURL(t *testing.T) {
	u, canon := NormalizeURL("HTTPS://Example.COM/Path/#frag")
	if u == nil {
		t.Fatal("expected valid url")
	}
	if canon != "https://example.com/Path/" {
		t.Errorf("canon = %q", canon)
	}
	if _, canon := NormalizeURL("javascript:alert(1)"); canon != "" {
		t.Errorf("javascript url should be rejected, got %q", canon)
	}
	if _, canon := NormalizeURL(""); canon != "" {
		t.Errorf("empty url should be rejected")
	}
}

func TestHostKeyStripsTracking(t *testing.T) {
	_, canon := NormalizeURL("https://example.com/a?utm_source=x&id=7&fbclid=y")
	u, _ := NormalizeURL(canon)
	key := HostKey(u)
	if strings.Contains(key, "utm_source") || strings.Contains(key, "fbclid") {
		t.Errorf("tracking params not stripped: %q", key)
	}
	if !strings.Contains(key, "id=7") {
		t.Errorf("meaningful param lost: %q", key)
	}
}

func newTestProvider(t *testing.T, cfg config.ProviderConfig) Provider {
	t.Helper()
	if cfg.Timeout.D() <= 0 {
		cfg.Timeout = config.Duration(2 * time.Second)
	}
	p, err := New(cfg, "test-agent")
	if err != nil {
		t.Fatalf("New(%s): %v", cfg.Type, err)
	}
	return p
}

func mustQ(t *testing.T, raw string) query.Query {
	t.Helper()
	q, err := query.Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return q
}

func TestSearXNG(t *testing.T) {
	var gotQuery, gotFormat, gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		gotLang = r.URL.Query().Get("language")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"url":"https://example.com/a?utm_source=x","title":"Hello &amp; World","content":"desc <b>bold</b>","publishedDate":"2026-10-01T00:00:00+00:00","score":1.4},
			{"url":"javascript:alert(1)","title":"bad","content":"x"},
			{"url":"https://other.com/b","title":"filtered out","content":"y"}
		]}`))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "searxng", Type: "searxng", BaseURL: srv.URL})
	res, err := p.Search(context.Background(), mustQ(t, `hello "exact" site:example.com lang:zh`))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("len = %d, want 1", len(res))
	}
	if res[0].Title != "Hello & World" {
		t.Errorf("Title = %q", res[0].Title)
	}
	if res[0].Snippet != "desc bold" {
		t.Errorf("Snippet = %q", res[0].Snippet)
	}
	if strings.Contains(res[0].URL, "utm_source") {
		t.Errorf("URL not normalized: %q", res[0].URL)
	}
	if res[0].Date.IsZero() {
		t.Error("date not parsed")
	}
	if gotFormat != "json" {
		t.Errorf("format param = %q", gotFormat)
	}
	if gotLang != "zh" {
		t.Errorf("language param = %q", gotLang)
	}
	for _, want := range []string{"hello", `"exact"`, "site:example.com"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("engine query %q missing %q", gotQuery, want)
		}
	}
}

func TestBingRSS(t *testing.T) {
	var gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFormat = r.URL.Query().Get("format")
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8" ?><rss version="2.0"><channel>
<title>bing</title>
<item><title>Post &amp; Title</title><link>https://example.com/p</link>
<description>Desc &lt;b&gt;html&lt;/b&gt;</description>
<pubDate>Sat, 17 Oct 2026 12:42:00 GMT</pubDate></item>
</channel></rss>`))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "bing", Type: "bingrss", BaseURL: srv.URL})
	res, err := p.Search(context.Background(), mustQ(t, "hello"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("len = %d, want 1", len(res))
	}
	if res[0].Title != "Post & Title" {
		t.Errorf("Title = %q", res[0].Title)
	}
	if res[0].Date.IsZero() || res[0].Date.Year() != 2026 {
		t.Errorf("Date = %v", res[0].Date)
	}
	if gotFormat != "rss" {
		t.Errorf("format = %q", gotFormat)
	}
}

func TestBingRSSRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>challenge</body></html>"))
	}))
	defer srv.Close()
	p := newTestProvider(t, config.ProviderConfig{Name: "bing", Type: "bingrss", BaseURL: srv.URL})
	if _, err := p.Search(context.Background(), mustQ(t, "hello")); err == nil {
		t.Fatal("expected error for HTML response")
	}
}

func TestDuckDuckGo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/html/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&amp;rut=xyz">DDG Title</a>
  <a class="result__snippet">DDG snippet here</a>
</div>
<div class="result">
  <a class="result__a" href="https://other.com/b">Second</a>
  <a class="result__snippet">Second snippet</a>
</div>
</body></html>`))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "ddg", Type: "duckduckgo", BaseURL: srv.URL})
	res, err := p.Search(context.Background(), mustQ(t, "hello"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("len = %d, want 2", len(res))
	}
	if res[0].URL != "https://example.com/a" {
		t.Errorf("unwrapped URL = %q", res[0].URL)
	}
	if res[0].Title != "DDG Title" || res[0].Snippet != "DDG snippet here" {
		t.Errorf("result = %+v", res[0])
	}
}

func TestDuckDuckGoChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div class="anomaly-modal">challenge</div></body></html>`))
	}))
	defer srv.Close()
	p := newTestProvider(t, config.ProviderConfig{Name: "ddg", Type: "duckduckgo", BaseURL: srv.URL})
	if _, err := p.Search(context.Background(), mustQ(t, "hello")); err == nil {
		t.Fatal("expected challenge error")
	}
}

func TestBrave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "brave-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/res/v1/web/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"Brave Hit","url":"https://example.com/b","description":"desc","page_age":"2026-10-02T00:00:00Z"}
		]}}`))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "brave", Type: "brave", APIKey: "brave-key", BaseURL: srv.URL})
	res, err := p.Search(context.Background(), mustQ(t, "hello"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Title != "Brave Hit" || res[0].Date.IsZero() {
		t.Fatalf("results = %+v", res)
	}
}

func TestTavily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tavily-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["query"] == "" {
			t.Error("empty query")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Tavily Hit","url":"https://example.com/t","content":"tavily desc","score":0.9,"published_date":"2026-10-03"}
		]}`))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "tavily", Type: "tavily", APIKey: "tavily-key", BaseURL: srv.URL})
	res, err := p.Search(context.Background(), mustQ(t, "hello site:example.com"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Title != "Tavily Hit" {
		t.Fatalf("results = %+v", res)
	}
}

func TestUnknownType(t *testing.T) {
	if _, err := New(config.ProviderConfig{Name: "x", Type: "nope"}, "ua"); err == nil {
		t.Fatal("expected error for unknown type")
	}
}
