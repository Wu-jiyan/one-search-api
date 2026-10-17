package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wujiyan/one-search-api/internal/auth"
	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/fetchsvc"
	"github.com/wujiyan/one-search-api/internal/httpx"
	"github.com/wujiyan/one-search-api/internal/limit"
	"github.com/wujiyan/one-search-api/internal/obs"
	"github.com/wujiyan/one-search-api/internal/provider"
	"github.com/wujiyan/one-search-api/internal/query"
	"github.com/wujiyan/one-search-api/internal/search"
)

const testPageHTML = `<!DOCTYPE html><html><head><title>示例文章</title></head><body><article>
<h1>示例文章标题</h1>
<p>这是用于测试抓取接口的正文段落，内容需要足够长以通过最小正文长度检查。</p>
<p>第二个段落继续补充内容，确保抽取结果稳定，不会被认为是没有正文的页面。</p>
</article></body></html>`

type fakeProvider struct {
	mu      sync.Mutex
	calls   int
	results []provider.Result
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Type() string { return "fake" }

func (f *fakeProvider) Search(_ context.Context, _ query.Query) ([]provider.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
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

type testEnv struct {
	t      *testing.T
	srv    *httptest.Server
	page   *httptest.Server
	fake   *fakeProvider
	cfg    *config.Config
	client *http.Client
}

func defaultResults() []provider.Result {
	return []provider.Result{
		{
			Title:   "DeepSeek 发布",
			URL:     "https://github.com/deepseek-ai/x",
			Snippet: "GitHub 上的项目公告",
			Engine:  "fake",
			Rank:    1,
			Date:    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			Title:   "第二篇",
			URL:     "https://example.com/b",
			Snippet: "第二篇的摘要内容",
			Engine:  "fake",
			Rank:    2,
		},
	}
}

func newEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()

	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(testPageHTML))
	}))

	cfg := config.Default()
	cfg.Auth = config.AuthConfig{
		Mode: "key",
		Keys: []config.KeyConfig{{
			Name: "tester", Token: "test-key", RPM: 1000, Burst: 100, MaxResults: 50,
		}},
	}
	cfg.Fetch.AllowPrivate = true
	cfg.Fetch.MinContentRunes = 40
	cfg.Fetch.Timeout = config.Duration(3 * time.Second)
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	// The page httptest server uses a random port; allow it through the guard.
	if u, err := url.Parse(pageSrv.URL); err == nil {
		if port, err := strconv.Atoi(u.Port()); err == nil {
			cfg.Fetch.AllowPorts = append(cfg.Fetch.AllowPorts, port)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := obs.NewMetrics()
	fake := &fakeProvider{results: defaultResults()}
	fetchSvc := fetchsvc.New(cfg, metrics)
	src := &search.Source{Name: "fake", Type: "fake", Weight: 1, Prov: fake, Breaker: limit.NewBreaker(5, time.Second)}
	searchSvc := search.New(cfg, []*search.Source{src}, fetchSvc, metrics, logger)
	handler := New(cfg, auth.New(cfg.Auth), searchSvc, fetchSvc, metrics, logger, "test").Handler()
	srv := httptest.NewServer(handler)

	env := &testEnv{t: t, srv: srv, page: pageSrv, fake: fake, cfg: cfg, client: srv.Client()}
	t.Cleanup(func() {
		srv.Close()
		pageSrv.Close()
	})
	return env
}

func (e *testEnv) call(method, path, token string, payload any) (int, http.Header, []byte) {
	e.t.Helper()
	var body io.Reader
	if payload != nil {
		switch v := payload.(type) {
		case string:
			body = strings.NewReader(v)
		default:
			raw, err := json.Marshal(v)
			if err != nil {
				e.t.Fatalf("marshal payload: %v", err)
			}
			body = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, data
}

func decodeError(t *testing.T, body []byte) httpx.ErrorBody {
	t.Helper()
	var eb httpx.ErrorBody
	if err := json.Unmarshal(body, &eb); err != nil {
		t.Fatalf("decode error body %q: %v", string(body), err)
	}
	return eb
}

func TestSearchHappyPath(t *testing.T) {
	env := newEnv(t, nil)
	status, header, body := env.call(http.MethodPost, "/v1/tools/search", "test-key",
		map[string]any{"query": "deepseek", "count": 5})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if ct := header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}

	var out struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(out.Results))
	}
	wantKeys := []string{
		"rerank_score", "title", "url", "snippet", "summary", "content",
		"site_name", "site_icon", "date", "authority",
	}
	for i, item := range out.Results {
		if len(item) != len(wantKeys) {
			t.Errorf("result[%d] has %d fields, want %d: %v", i, len(item), len(wantKeys), item)
		}
		for _, k := range wantKeys {
			if _, ok := item[k]; !ok {
				t.Errorf("result[%d] missing field %q", i, k)
			}
		}
	}

	byTitle := map[string]map[string]json.RawMessage{}
	for _, item := range out.Results {
		var title string
		_ = json.Unmarshal(item["title"], &title)
		byTitle[title] = item
	}

	gh, ok := byTitle["DeepSeek 发布"]
	if !ok {
		t.Fatalf("github result missing; titles = %v", keysOf(byTitle))
	}
	var score float64
	if err := json.Unmarshal(gh["rerank_score"], &score); err != nil {
		t.Fatalf("score: %v", err)
	}
	if score < 0 || score > 3 {
		t.Errorf("score = %v, out of range", score)
	}
	var authority int
	_ = json.Unmarshal(gh["authority"], &authority)
	if authority != 3 {
		t.Errorf("authority = %d, want 3", authority)
	}
	var siteName string
	if err := json.Unmarshal(gh["site_name"], &siteName); err != nil || siteName != "GitHub" {
		t.Errorf("site_name = %q, err = %v", siteName, err)
	}
	var content string
	if err := json.Unmarshal(gh["content"], &content); err != nil || content != "" {
		t.Errorf("content = %q, err = %v; want empty without need_content", content, err)
	}
	var dateStr string
	if err := json.Unmarshal(gh["date"], &dateStr); err != nil {
		t.Fatalf("date is not a string: %s", gh["date"])
	}
	if _, err := time.Parse(time.RFC3339, dateStr); err != nil {
		t.Errorf("date %q not RFC3339: %v", dateStr, err)
	}

	second, ok := byTitle["第二篇"]
	if !ok {
		t.Fatal("second result missing")
	}
	if string(second["date"]) != "null" {
		t.Errorf("undated result date = %s, want null", second["date"])
	}
	if string(second["content"]) != `""` {
		t.Errorf("content = %s, want empty string", second["content"])
	}
}

func keysOf(m map[string]map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSearchEmptyResults(t *testing.T) {
	env := newEnv(t, nil)
	env.fake.setResults(nil)
	status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key",
		map[string]any{"query": "no-hits-here"})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	var out struct {
		Results json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(out.Results) != "[]" {
		t.Errorf("results = %s, want []", out.Results)
	}
}

func TestSearchValidation(t *testing.T) {
	env := newEnv(t, nil)
	cases := []struct {
		name    string
		payload any
		want    int
	}{
		{"missing query", map[string]any{}, http.StatusBadRequest},
		{"blank query", map[string]any{"query": "   "}, http.StatusBadRequest},
		{"count zero", map[string]any{"query": "x", "count": 0}, http.StatusBadRequest},
		{"count too big", map[string]any{"query": "x", "count": 51}, http.StatusBadRequest},
		{"ori_question too long", map[string]any{"query": "x", "ori_question": strings.Repeat("q", 200)}, http.StatusBadRequest},
		{"invalid json", "{not json", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", tc.payload)
			if status != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", status, tc.want, body)
			}
			eb := decodeError(t, body)
			if eb.Error.Message == "" || eb.Error.Type != httpx.TypeInvalidRequest {
				t.Errorf("error = %+v", eb.Error)
			}
		})
	}
}

func TestSearchIgnoresUnknownFields(t *testing.T) {
	env := newEnv(t, nil)
	status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key",
		map[string]any{"query": "unknown-fields", "totally_unknown": 42, "localization": "1.2.3.4"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
}

func TestPayloadTooLarge(t *testing.T) {
	env := newEnv(t, nil)
	big := strings.Repeat("a", int(env.cfg.Server.MaxBodyBytes)+4096)
	status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", `{"query":"`+big+`"}`)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413, body = %s", status, body)
	}
	eb := decodeError(t, body)
	if eb.Error.Code != "payload_too_large" {
		t.Errorf("code = %q", eb.Error.Code)
	}
}

func TestDecodeErrorMapping(t *testing.T) {
	if e := decodeBodyError(errors.New("bad json")); e.Status != http.StatusBadRequest {
		t.Errorf("generic decode error -> %d", e.Status)
	}
	if e := decodeBodyError(context.DeadlineExceeded); e.Status != http.StatusRequestTimeout || e.Code != "request_timeout" {
		t.Errorf("timeout decode error -> %+v", e)
	}
}

func TestAuthRequired(t *testing.T) {
	env := newEnv(t, nil)
	status, _, body := env.call(http.MethodPost, "/v1/tools/search", "",
		map[string]any{"query": "x"})
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	eb := decodeError(t, body)
	if eb.Error.Type != httpx.TypeAuthentication || eb.Error.Code != "invalid_api_key" {
		t.Errorf("error = %+v", eb.Error)
	}
	if eb.Error.Param != nil {
		t.Errorf("param = %v, want null", eb.Error.Param)
	}
}

func TestRateLimit(t *testing.T) {
	env := newEnv(t, func(c *config.Config) {
		c.Auth.Keys[0].RPM = 1
		c.Auth.Keys[0].Burst = 1
		c.Auth.Keys[0].DailyQuota = 0
	})
	if status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", map[string]any{"query": "rl-1"}); status != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", status, body)
	}
	status, header, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", map[string]any{"query": "rl-2"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429, body = %s", status, body)
	}
	if header.Get("Retry-After") == "" {
		t.Error("missing Retry-After header")
	}
	eb := decodeError(t, body)
	if eb.Error.Type != httpx.TypeRateLimit {
		t.Errorf("error type = %q", eb.Error.Type)
	}
}

func TestDailyQuota(t *testing.T) {
	env := newEnv(t, func(c *config.Config) {
		c.Auth.Keys[0].RPM = 1000
		c.Auth.Keys[0].Burst = 100
		c.Auth.Keys[0].DailyQuota = 1
	})
	if status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", map[string]any{"query": "quota-1"}); status != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", status, body)
	}
	status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", map[string]any{"query": "quota-2"})
	if status != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body = %s", status, body)
	}
	eb := decodeError(t, body)
	if eb.Error.Code != "insufficient_quota" {
		t.Errorf("code = %q", eb.Error.Code)
	}
}

func TestFetchHappyPath(t *testing.T) {
	env := newEnv(t, nil)
	status, _, body := env.call(http.MethodPost, "/v1/tools/fetch", "test-key", map[string]any{"url": env.page.URL})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var out httpx.FetchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out.Title, "示例文章") {
		t.Errorf("title = %q", out.Title)
	}
	if !strings.Contains(out.Content, "正文段落") {
		t.Errorf("content = %q", out.Content)
	}
}

func TestFetchSSRFBlocked(t *testing.T) {
	env := newEnv(t, func(c *config.Config) { c.Fetch.AllowPrivate = false })
	status, _, body := env.call(http.MethodPost, "/v1/tools/fetch", "test-key", map[string]any{"url": env.page.URL})
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", status, body)
	}
	eb := decodeError(t, body)
	if eb.Error.Type != httpx.TypePermission {
		t.Errorf("error type = %q", eb.Error.Type)
	}
}

func TestFetchValidation(t *testing.T) {
	env := newEnv(t, nil)
	cases := []struct {
		payload any
		want    int
	}{
		{map[string]any{}, http.StatusBadRequest},
		{map[string]any{"url": "file:///etc/passwd"}, http.StatusBadRequest},
		{map[string]any{"url": "javascript:alert(1)"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		status, _, body := env.call(http.MethodPost, "/v1/tools/fetch", "test-key", tc.payload)
		if status != tc.want {
			t.Errorf("payload %v: status = %d, want %d, body = %s", tc.payload, status, tc.want, body)
		}
	}
}

func TestNeedContentViaAPI(t *testing.T) {
	env := newEnv(t, nil)
	env.fake.setResults([]provider.Result{{
		Title:  "示例文章",
		URL:    env.page.URL,
		Engine: "fake",
		Rank:   1,
	}})
	status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key",
		map[string]any{"query": "need-content-test", "need_content": true})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var out httpx.SearchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) != 1 {
		t.Fatalf("results = %d", len(out.Results))
	}
	if out.Results[0].Content == "" {
		t.Error("content should be populated with need_content=true")
	}
	if out.Results[0].Summary == "" {
		t.Error("summary should be populated")
	}
}

func TestProviderAllowlistFilters(t *testing.T) {
	env := newEnv(t, func(c *config.Config) {
		c.Auth.Keys[0].AllowedProviders = []string{"other"}
	})
	status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", map[string]any{"query": "allowlist-test"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var out struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Results) != 0 {
		t.Errorf("results = %d, want 0 (provider filtered)", len(out.Results))
	}
}

func TestSearchCacheHit(t *testing.T) {
	env := newEnv(t, nil)
	payload := map[string]any{"query": "cache-hit-test"}
	if status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", payload); status != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", status, body)
	}
	if status, _, body := env.call(http.MethodPost, "/v1/tools/search", "test-key", payload); status != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", status, body)
	}
	if got := env.fake.callCount(); got != 1 {
		t.Errorf("provider calls = %d, want 1", got)
	}
}

func TestHealthz(t *testing.T) {
	env := newEnv(t, nil)
	status, _, body := env.call(http.MethodGet, "/healthz", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var out struct {
		Status    string `json:"status"`
		Version   string `json:"version"`
		Providers []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != "ok" || out.Version != "test" {
		t.Errorf("healthz = %+v", out)
	}
	if len(out.Providers) != 1 || out.Providers[0].Name != "fake" {
		t.Errorf("providers = %+v", out.Providers)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	env := newEnv(t, nil)
	_, _, _ = env.call(http.MethodPost, "/v1/tools/search", "test-key", map[string]any{"query": "metrics-test"})
	status, _, body := env.call(http.MethodGet, "/metrics", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), "onesearch_requests_total") {
		t.Errorf("metrics output missing counters: %s", body)
	}
}

func TestUnknownEndpoint(t *testing.T) {
	env := newEnv(t, nil)
	status, _, body := env.call(http.MethodGet, "/v1/nope", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	eb := decodeError(t, body)
	if eb.Error.Type != httpx.TypeNotFound {
		t.Errorf("error type = %q", eb.Error.Type)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	env := newEnv(t, nil)
	status, _, _ := env.call(http.MethodGet, "/v1/tools/search", "test-key", nil)
	if status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", status)
	}
}

func TestRequestIDHeader(t *testing.T) {
	env := newEnv(t, nil)
	_, header, _ := env.call(http.MethodGet, "/healthz", "", nil)
	if header.Get("X-Request-Id") == "" {
		t.Error("missing X-Request-Id header")
	}
}
