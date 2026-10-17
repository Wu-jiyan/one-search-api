//go:build live

package provider

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/wujiyan/one-search-api/internal/config"
	"github.com/wujiyan/one-search-api/internal/query"
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
