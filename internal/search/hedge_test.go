package search

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/fetchsvc"
	"github.com/Wu-jiyan/one-search-api/internal/obs"
	"github.com/Wu-jiyan/one-search-api/internal/provider"
)

func newTestServiceMulti(t *testing.T, cfg *config.Config, srcs []*Source) *Service {
	t.Helper()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := obs.NewMetrics()
	return New(cfg, srcs, fetchsvc.New(cfg, metrics), metrics, logger)
}

func resultsOn(domain string, n int, engine string) []provider.Result {
	out := make([]provider.Result, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, provider.Result{
			Title:  domain + " item " + string(rune('a'+i)),
			URL:    "https://" + domain + "/" + string(rune('a'+i)),
			Engine: engine,
			Rank:   i + 1,
		})
	}
	return out
}

func TestHedgeStopsWaitingForSlowProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Fetch.AllowPrivate = true
	cfg.Search.HedgeDelay = config.Duration(100 * time.Millisecond)
	cfg.Search.ProviderTimeout = config.Duration(3 * time.Second)
	cfg.Search.OverallTimeout = config.Duration(5 * time.Second)

	fast := &fakeProvider{results: resultsOn("fast.example", 5, "fast")}
	slow := &fakeProvider{delay: 3 * time.Second}
	srcs := []*Source{
		{Name: "fast", Type: "fake", Weight: 1, Timeout: 250 * time.Millisecond, Prov: fast},
		{Name: "slow", Type: "fake", Weight: 1, Timeout: 3 * time.Second, Prov: slow},
	}
	svc := newTestServiceMulti(t, cfg, srcs)

	start := time.Now()
	res, err := svc.Search(context.Background(), mustQ(t, "hedge-test"), 5, false, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) < 5 {
		t.Errorf("len = %d, want >= 5", len(res))
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("hedge did not cut latency, elapsed = %v", elapsed)
	}
}

func TestNoHedgeBelowThreshold(t *testing.T) {
	cfg := config.Default()
	cfg.Fetch.AllowPrivate = true
	cfg.Search.HedgeDelay = config.Duration(50 * time.Millisecond)
	cfg.Search.ProviderTimeout = config.Duration(2 * time.Second)

	fast := &fakeProvider{results: resultsOn("fast.example", 1, "fast")}
	slow := &fakeProvider{delay: 200 * time.Millisecond, results: resultsOn("slow.example", 1, "slow")}
	srcs := []*Source{
		{Name: "fast", Type: "fake", Weight: 1, Timeout: time.Second, Prov: fast},
		{Name: "slow", Type: "fake", Weight: 1, Timeout: 2 * time.Second, Prov: slow},
	}
	svc := newTestServiceMulti(t, cfg, srcs)

	start := time.Now()
	res, err := svc.Search(context.Background(), mustQ(t, "no-hedge-test"), 10, false, nil, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Errorf("len = %d, want 2 (waited for slow provider)", len(res))
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("returned too early, elapsed = %v", elapsed)
	}
}
