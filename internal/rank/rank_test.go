package rank

import (
	"testing"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/provider"
	"github.com/Wu-jiyan/one-search-api/internal/query"
)

func mustQuery(t *testing.T, raw string) query.Query {
	t.Helper()
	q, err := query.Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return q
}

func TestFuseDedupesAndMergesSources(t *testing.T) {
	results := []provider.Result{
		{Title: "A", URL: "https://example.com/a?utm_source=x", Engine: "e1", Rank: 1, Snippet: "short"},
		{Title: "A", URL: "https://example.com/a", Engine: "e2", Rank: 2, Snippet: "a much longer snippet"},
		{Title: "B", URL: "https://other.com/b", Engine: "e1", Rank: 2},
	}
	items := Fuse(results, mustQuery(t, "test"), Options{})
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	var a *Item
	for i := range items {
		if items[i].Title == "A" {
			a = &items[i]
		}
	}
	if a == nil {
		t.Fatal("merged item A not found")
	}
	if len(a.Sources) != 2 {
		t.Errorf("Sources = %v, want 2 entries", a.Sources)
	}
	if a.Snippet != "a much longer snippet" {
		t.Errorf("Snippet = %q, want the longer one", a.Snippet)
	}
	if len(a.Snippets) != 2 {
		t.Errorf("Snippets = %v, want 2", a.Snippets)
	}
	// Higher-ranked item from the stronger source should be first.
	if items[0].Title != "A" {
		t.Errorf("order = %v, want A first", []string{items[0].Title, items[1].Title})
	}
}

func TestFuseFilters(t *testing.T) {
	results := []provider.Result{
		{Title: "keep", URL: "https://example.com/keep", Engine: "e1", Rank: 1},
		{Title: "blocked site", URL: "https://blocked.com/x", Engine: "e1", Rank: 2},
		{Title: "too old", URL: "https://example.com/old", Engine: "e1", Rank: 3,
			Date: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Title: "bad scheme", URL: "javascript:alert(1)", Engine: "e1", Rank: 4},
	}
	q := mustQuery(t, "keep after:2025 -site:blocked.com")
	items := Fuse(results, q, Options{})
	if len(items) != 1 || items[0].Title != "keep" {
		names := make([]string, len(items))
		for i, it := range items {
			names[i] = it.Title
		}
		t.Fatalf("filtered items = %v, want [keep]", names)
	}
}

func TestFuseScoreRangeAndOrdering(t *testing.T) {
	results := []provider.Result{
		{Title: "best match", URL: "https://official.gov.cn/doc", Engine: "searxng", Rank: 1,
			Date: time.Now().Add(-24 * time.Hour)},
		{Title: "meh", URL: "https://forum.example.com/x", Engine: "searxng", Rank: 5},
	}
	items := Fuse(results, mustQuery(t, "best match"), Options{PerDomainLimit: 3})
	if len(items) != 2 {
		t.Fatalf("len = %d", len(items))
	}
	for _, it := range items {
		if it.Score < 0 || it.Score > 3 {
			t.Errorf("score out of range: %v", it.Score)
		}
	}
	if items[0].Score < items[1].Score {
		t.Errorf("results not ordered: %v < %v", items[0].Score, items[1].Score)
	}
	if items[0].Authority != 4 {
		t.Errorf("gov.cn authority = %d, want 4", items[0].Authority)
	}
}

func TestFusePerDomainLimit(t *testing.T) {
	results := []provider.Result{
		{Title: "a", URL: "https://example.com/1", Engine: "e1", Rank: 1},
		{Title: "b", URL: "https://example.com/2", Engine: "e1", Rank: 2},
		{Title: "c", URL: "https://example.com/3", Engine: "e1", Rank: 3},
		{Title: "d", URL: "https://other.com/4", Engine: "e1", Rank: 4},
	}
	items := Fuse(results, mustQuery(t, "test"), Options{PerDomainLimit: 1})
	if len(items) != 4 {
		t.Fatalf("len = %d, want 4 (deferred results appended)", len(items))
	}
	if items[0].Title != "a" {
		t.Errorf("first = %q", items[0].Title)
	}
	if items[1].Title != "d" {
		t.Errorf("second = %q, want the other domain first", items[1].Title)
	}
}

func TestFuseEmpty(t *testing.T) {
	if items := Fuse(nil, mustQuery(t, "x"), Options{}); len(items) != 0 {
		t.Errorf("expected no items, got %d", len(items))
	}
}
