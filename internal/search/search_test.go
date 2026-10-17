package search

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/fetchsvc"
	"github.com/wujiyan/one-search-api/internal/obs"
	"github.com/wujiyan/one-search-api/internal/provider"
	"github.com/wujiyan/one-search-api/internal/query"
)

type fakeProvider struct {
	mu      sync.Mutex
	calls   int
	results []provider.Result
	delay   time.Duration
	err     error
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Type() string { return "fake" }

func (f *fakeProvider) Search(ctx context.Context, _ query.Query) ([]provider.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProvider) setResults(r []provider.Result) {
	f.mu.Lock()
	f.results = r
	f.mu.Unlock()
}

const testPageHTML = `<!DOCTYPE html><html><head><title>示例文章</title></head><body><article>
<h1>示例文章</h1>
<p>这是用于测试正文抓取的段落，内容需要足够长以通过最小正文长度检查。</p>
<p>第二个段落继续补充内容，让可读性抽取得到稳定结果，避免被判定为空正文。</p>
</article></body></html>`

func newTestService(t *testing.T, f *fakeProvider, mutate func(*config.Config)) *Service {
	t.Helper()
	cfg := config.Default()
	cfg.Fetch.AllowPrivate = true
	cfg.Fetch.MinContentRunes = 40
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := obs.NewMetrics()
	fetch := fetchsvc.New(cfg, metrics)
	src := &Source{
		Name:    "fake",
		Type:    "fake",
		Weight:  1,
		Prov:    f,
		Breaker: nil,
	}
	return New(cfg, []*Source{src}, fetch, metrics, logger)
}

func mustQ(t *testing.T, raw string) query.Query {
	t.Helper()
	q, err := query.Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return q
}

func TestSearchBasic(t *testing.T) {
	f := &fakeProvider{results: []provider.Result{
		{Title: "DeepSeek 发布", URL: "https://github.com/deepseek-ai/x", Snippet: "摘要", Engine: "fake", Rank: 1,
			Date: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{Title: "第二篇", URL: "https://example.com/b", Snippet: "第二篇摘要", Engine: "fake", Rank: 2},
	}}
	svc := newTestService(t, f, nil)

	res, err := svc.Search(context.Background(), mustQ(t, "deepseek"), 10, false, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("len = %d, want 2", len(res))
	}
	for _, r := range res {
		if r.RerankScore < 0 || r.RerankScore > 3 {
			t.Errorf("score out of range: %v", r.RerankScore)
		}
		if r.Content != "" {
			t.Errorf("content should be empty without need_content: %q", r.Content)
		}
		if r.Summary == "" {
			t.Errorf("summary should not be empty for %q", r.Title)
		}
		if r.SiteName == nil || r.SiteIcon == nil {
			t.Errorf("site metadata missing for %q", r.Title)
		}
	}
	var gh *httpxResult
	for i := range res {
		if res[i].URL == "https://github.com/deepseek-ai/x" {
			gh = &httpxResult{score: res[i].RerankScore, authority: res[i].Authority, date: res[i].Date}
		}
	}
	if gh == nil {
		t.Fatal("github result not found")
	}
	if gh.authority != 3 {
		t.Errorf("authority = %d, want 3", gh.authority)
	}
	if gh.date == nil {
		t.Error("date should be set")
	}
}

type httpxResult struct {
	score     float64
	authority int
	date      *time.Time
}

func TestSearchCache(t *testing.T) {
	f := &fakeProvider{results: []provider.Result{
		{Title: "A", URL: "https://example.com/a", Engine: "fake", Rank: 1},
	}}
	svc := newTestService(t, f, nil)
	q := mustQ(t, "cache-test")

	if _, err := svc.Search(context.Background(), q, 5, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Search(context.Background(), q, 5, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1 (cache hit)", got)
	}
}

func TestSearchNeedContent(t *testing.T) {
	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(testPageHTML))
	}))
	defer pageSrv.Close()
	u, _ := url.Parse(pageSrv.URL)
	port, _ := strconv.Atoi(u.Port())

	f := &fakeProvider{results: []provider.Result{
		{Title: "示例文章", URL: pageSrv.URL, Engine: "fake", Rank: 1},
	}}
	svc := newTestService(t, f, func(c *config.Config) {
		c.Fetch.AllowPorts = []int{port}
	})

	res, err := svc.Search(context.Background(), mustQ(t, "content-test"), 5, true, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("len = %d", len(res))
	}
	if res[0].Content == "" {
		t.Error("content should be fetched when need_content=true")
	}
	if res[0].Title != "示例文章" {
		t.Errorf("title = %q", res[0].Title)
	}
}

func TestSearchProviderTimeoutDegrades(t *testing.T) {
	f := &fakeProvider{delay: 200 * time.Millisecond, results: []provider.Result{
		{Title: "late", URL: "https://example.com/late", Engine: "fake", Rank: 1},
	}}
	svc := newTestService(t, f, func(c *config.Config) {
		c.Search.ProviderTimeout = config.Duration(20 * time.Millisecond)
		c.Search.OverallTimeout = config.Duration(200 * time.Millisecond)
	})

	start := time.Now()
	res, err := svc.Search(context.Background(), mustQ(t, "timeout-test"), 5, false, nil)
	if err != nil {
		t.Fatalf("Search should degrade, got error: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("len = %d, want 0", len(res))
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("provider timeout not applied, elapsed = %v", elapsed)
	}
}

func TestSearchAllowedProviders(t *testing.T) {
	f := &fakeProvider{results: []provider.Result{
		{Title: "A", URL: "https://example.com/a", Engine: "fake", Rank: 1},
	}}
	svc := newTestService(t, f, nil)
	res, err := svc.Search(context.Background(), mustQ(t, "allow-test"), 5, false, map[string]bool{"other": true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("len = %d, want 0 for disallowed provider", len(res))
	}
	if got := f.callCount(); got != 0 {
		t.Errorf("provider calls = %d, want 0", got)
	}
}

func TestProviderStates(t *testing.T) {
	f := &fakeProvider{}
	svc := newTestService(t, f, nil)
	states := svc.ProviderStates()
	if len(states) != 1 || states[0].Name != "fake" || states[0].State != "closed" {
		t.Fatalf("states = %+v", states)
	}
}

func TestSearchCountClamp(t *testing.T) {
	f := &fakeProvider{results: []provider.Result{
		{Title: "A", URL: "https://example.com/a", Engine: "fake", Rank: 1},
		{Title: "B", URL: "https://example.com/b", Engine: "fake", Rank: 2},
	}}
	svc := newTestService(t, f, func(c *config.Config) { c.Search.MaxCount = 1 })
	res, err := svc.Search(context.Background(), mustQ(t, "clamp-test"), 10, false, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Errorf("len = %d, want 1 (max_count)", len(res))
	}
}
