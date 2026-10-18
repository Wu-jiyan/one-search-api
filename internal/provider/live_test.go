//go:build live

package provider

import (
	"context"
	"encoding/xml"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/query"
)

// Opt-in network tests:
//
//	go test ./internal/provider -tags live -run TestLiveBingCN -v -count=1
//	ONESEARCH_LIVE_INTL=1 go test ./internal/provider -tags live -run TestLiveBingIntl -v -count=1
//
// The intl variant is meant to be run from a non-mainland host (e.g. the HK
// server): cn.bing.com redirects overseas, www.bing.com is the intl endpoint.

func liveProviderConfig(typ, base, market string, mobile bool) config.ProviderConfig {
	return config.ProviderConfig{
		Name:    typ,
		Type:    typ,
		BaseURL: base,
		Market:  market,
		Mobile:  mobile,
		Timeout: config.Duration(15 * time.Second),
	}
}

func runLiveQueries(t *testing.T, cfg config.ProviderConfig, queries []string) {
	t.Helper()
	p, err := New(cfg, "")
	if err != nil {
		t.Fatalf("New(%s): %v", cfg.Type, err)
	}
	for _, raw := range queries {
		q, perr := query.Parse(raw)
		if perr != nil {
			t.Fatalf("parse %q: %v", raw, perr)
		}
		start := time.Now()
		res, serr := p.Search(context.Background(), q)
		elapsed := time.Since(start)
		if serr != nil {
			t.Errorf("[%s] %q: error = %v", cfg.Name, raw, serr)
			continue
		}
		t.Logf("[%s] %q -> %d results in %s", cfg.Name, raw, len(res), elapsed.Round(time.Millisecond))
		if len(res) == 0 {
			t.Errorf("[%s] %q returned no results", cfg.Name, raw)
			continue
		}
		for i, r := range res {
			if i >= 3 {
				break
			}
			t.Logf("   %-55s | %s | date=%v", r.Title, r.URL, r.Date.Format(time.RFC3339))
		}
	}
}

func TestLiveBingCN(t *testing.T) {
	debugSearch = t.Logf
	defer func() { debugSearch = nil }()

	queries := []string{
		"golang context 取消 propagate",
		"DeepSeek V4 发布",
		"rust ownership",
	}
	runLiveQueries(t, liveProviderConfig("bingrss", "https://cn.bing.com", "zh-CN", false), queries)
	runLiveQueries(t, liveProviderConfig("binghtml", "https://cn.bing.com", "zh-CN", false), queries[:2])
}

func TestLiveBingIntl(t *testing.T) {
	if os.Getenv("ONESEARCH_LIVE_INTL") == "" {
		t.Skip("set ONESEARCH_LIVE_INTL=1 to run against www.bing.com")
	}
	debugSearch = t.Logf
	defer func() { debugSearch = nil }()

	queries := []string{"golang context cancellation", "deepseek v4 release notes"}
	runLiveQueries(t, liveProviderConfig("bingrss", "https://www.bing.com", "en-WW", false), queries)
	runLiveQueries(t, liveProviderConfig("binghtml", "https://www.bing.com", "en-WW", false), queries)
}

func TestLiveBingCNMobile(t *testing.T) {
	debugSearch = t.Logf
	defer func() { debugSearch = nil }()
	runLiveQueries(t, liveProviderConfig("bingrss", "https://cn.bing.com", "zh-CN", true), []string{"你好"})
}

// TestLiveBingPagination answers, against the live endpoint, whether Bing
// honours the &first= pagination parameter for our session. It logs the result
// rather than asserting, because Bing's behaviour differs by network/region.
//
//	go test ./internal/provider -tags live -run TestLiveBingPagination -v -count=1
func TestLiveBingPagination(t *testing.T) {
	debugSearch = t.Logf
	defer func() { debugSearch = nil }()

	cfg := config.ProviderConfig{
		Name:    "binghtml",
		Type:    "binghtml",
		BaseURL: "https://cn.bing.com",
		Market:  "zh-CN",
		Timeout: config.Duration(25 * time.Second),
	}
	// The intl endpoint may behave differently, so allow overriding the target.
	if v := os.Getenv("ONESEARCH_LIVE_BING_BASE"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("ONESEARCH_LIVE_BING_MARKET"); v != "" {
		cfg.Market = v
	}
	liveQuery := "golang context cancellation"
	if v := os.Getenv("ONESEARCH_LIVE_BING_QUERY"); v != "" {
		liveQuery = v
	}
	p, err := New(cfg, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	html, ok := p.(*bingHTML)
	if !ok {
		t.Fatalf("provider type = %T", p)
	}
	sess := html.sess
	ctx := context.Background()
	sess.Warm(ctx)

	q, err := query.Parse(liveQuery)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q.Count = 50

	fetch := func(first int, rss bool) []Result {
		t.Helper()
		target := sess.searchURLPage(q, "", rss, first)
		accept := "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
		if rss {
			accept = "application/rss+xml, application/xml;q=0.9, */*;q=0.1"
		}
		resp, err := sess.get(ctx, target, accept)
		if err != nil {
			t.Fatalf("first=%d: %v", first, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			t.Fatalf("first=%d read: %v", first, err)
		}
		t.Logf("[first=%d rss=%v] url=%s bytes=%d", first, rss, target, len(body))
		if rss {
			var doc rssDocument
			if err := xml.Unmarshal(body, &doc); err != nil {
				t.Logf("  rss decode: %v", err)
				return nil
			}
			out := make([]Result, 0, len(doc.Channel.Items))
			for _, item := range doc.Channel.Items {
				out = append(out, Result{Title: cleanText(item.Title), URL: item.Link})
			}
			return out
		}
		res, perr := parseBingHTML(body, "binghtml", q)
		if perr != nil {
			t.Logf("  parse: %v", perr)
		}
		return res
	}

	for _, rss := range []bool{false, true} {
		pages := map[int][]Result{}
		for _, first := range []int{1, 11, 21} {
			pages[first] = fetch(first, rss)
			time.Sleep(700 * time.Millisecond)
		}
		uniq := map[string]bool{}
		for _, first := range []int{1, 11, 21} {
			seen := map[string]bool{}
			for _, r := range pages[first] {
				uniq[r.URL] = true
				if !seen[r.URL] {
					seen[r.URL] = true
				}
			}
			t.Logf("rss=%v first=%-3d results=%d uniqueInPage=%d", rss, first, len(pages[first]), len(seen))
			for i, r := range pages[first] {
				if i >= 2 {
					break
				}
				t.Logf("    %s", r.URL)
			}
		}
		t.Logf("rss=%v TOTAL unique urls across pages = %d", rss, len(uniq))
	}
}

// TestLiveBingPaginationFill exercises the full provider path with count=50 and
// reports how many results pagination actually produced. On the international
// endpoint (run from outside mainland China) this should grow well past 10; on
// cn.bing.com it stops at the first repeated page.
//
//	ONESEARCH_LIVE_BING_BASE=https://www.bing.com ONESEARCH_LIVE_BING_MARKET=en-WW \
//	  go test ./internal/provider -tags live -run TestLiveBingPaginationFill -v -count=1
func TestLiveBingPaginationFill(t *testing.T) {
	debugSearch = t.Logf
	defer func() { debugSearch = nil }()

	cfg := config.ProviderConfig{
		Name:    "binghtml",
		Type:    "binghtml",
		BaseURL: "https://cn.bing.com",
		Market:  "zh-CN",
		Timeout: config.Duration(30 * time.Second),
	}
	if v := os.Getenv("ONESEARCH_LIVE_BING_BASE"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("ONESEARCH_LIVE_BING_MARKET"); v != "" {
		cfg.Market = v
	}
	if v := os.Getenv("ONESEARCH_LIVE_BING_TYPE"); v != "" {
		cfg.Type = v
	}
	p, err := New(cfg, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q, err := query.Parse("golang context cancellation")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q.Count = 50

	start := time.Now()
	res, serr := p.Search(context.Background(), q)
	t.Logf("base=%s market=%s count=50 -> results=%d elapsed=%s err=%v",
		cfg.BaseURL, cfg.Market, len(res), time.Since(start).Round(time.Millisecond), serr)
	seen := map[string]bool{}
	for _, r := range res {
		seen[r.URL] = true
	}
	t.Logf("unique urls = %d", len(seen))
	if len(res) <= 10 {
		t.Logf("NOTE: pagination added nothing (domestic endpoint repeats page 1)")
	}
}
