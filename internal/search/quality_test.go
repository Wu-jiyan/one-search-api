package search

import (
	"context"
	"strings"
	"testing"

	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/provider"
)

func qualityTestConfig() *config.Config {
	cfg := config.Default()
	cfg.Fetch.AllowPrivate = true
	return cfg
}

func TestStaleProviderBatchDropped(t *testing.T) {
	good := &fakeProvider{results: []provider.Result{
		{Title: "Go context cancellation", URL: "https://go.dev/a", Snippet: "context cancellation basics", Engine: "good", Rank: 1},
		{Title: "golang context timeout", URL: "https://go.dev/b", Snippet: "context timeout patterns", Engine: "good", Rank: 2},
		{Title: "Context propagation in Go", URL: "https://go.dev/c", Snippet: "context values", Engine: "good", Rank: 3},
	}}
	stale := &fakeProvider{results: []provider.Result{
		{Title: "21 must-try Vietnamese dishes", URL: "https://food.example/a", Engine: "stale", Rank: 1},
		{Title: "Vietnamese Food: Top 100 Dishes", URL: "https://food.example/b", Engine: "stale", Rank: 2},
		{Title: "Best Vietnam Food", URL: "https://food.example/c", Engine: "stale", Rank: 3},
	}}

	svc := newTestServiceMulti(t, qualityTestConfig(), []*Source{
		{Name: "good", Type: "fake", Weight: 1, Prov: good},
		{Name: "stale", Type: "fake", Weight: 1, Prov: stale},
	})
	res, err := svc.Search(context.Background(), mustQ(t, "golang context cancellation"), 10, false, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("expected results from the on-topic provider")
	}
	for _, r := range res {
		if strings.Contains(r.URL, "food.example") {
			t.Errorf("stale off-topic batch leaked into results: %s", r.URL)
		}
	}
}

func TestAllWeakBatchesKept(t *testing.T) {
	a := &fakeProvider{results: []provider.Result{
		{Title: "Some unrelated article", URL: "https://a.example/1", Engine: "a", Rank: 1},
	}}
	b := &fakeProvider{results: []provider.Result{
		{Title: "Another unrelated item", URL: "https://b.example/1", Engine: "b", Rank: 1},
	}}
	svc := newTestServiceMulti(t, qualityTestConfig(), []*Source{
		{Name: "a", Type: "fake", Weight: 1, Prov: a},
		{Name: "b", Type: "fake", Weight: 1, Prov: b},
	})
	res, err := svc.Search(context.Background(), mustQ(t, "golang context cancellation"), 10, false, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Without any on-topic provider we must not nuke the result set.
	if len(res) != 2 {
		t.Errorf("len = %d, want 2 (nothing should be discarded)", len(res))
	}
}

func TestSingleProviderOffTopicItemsFiltered(t *testing.T) {
	results := []provider.Result{
		{Title: "golang context cancellation guide", URL: "https://go.dev/a", Engine: "only", Rank: 1},
	}
	for i := 0; i < 6; i++ {
		results = append(results, provider.Result{
			Title:  "Vietnamese food item " + string(rune('a'+i)),
			URL:    "https://food.example/" + string(rune('a'+i)),
			Engine: "only",
			Rank:   i + 2,
		})
	}
	f := &fakeProvider{results: results}
	svc := newTestServiceMulti(t, qualityTestConfig(), []*Source{
		{Name: "only", Type: "fake", Weight: 1, Prov: f},
	})
	res, err := svc.Search(context.Background(), mustQ(t, "golang context cancellation"), 10, false, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("len = %d, want 1 after off-topic filtering", len(res))
	}
	if !strings.Contains(res[0].Title, "golang") {
		t.Errorf("wrong survivor: %q", res[0].Title)
	}
}
