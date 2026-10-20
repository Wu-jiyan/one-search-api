// Package search orchestrates provider fan-out, fusion, enrichment and the
// optional content fetch for the /v1/tools/search endpoint.
package search

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/cache"
	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/enrich"
	"github.com/Wu-jiyan/one-search-api/internal/extract"
	"github.com/Wu-jiyan/one-search-api/internal/fetchsvc"
	"github.com/Wu-jiyan/one-search-api/internal/httpx"
	"github.com/Wu-jiyan/one-search-api/internal/limit"
	"github.com/Wu-jiyan/one-search-api/internal/obs"
	"github.com/Wu-jiyan/one-search-api/internal/provider"
	"github.com/Wu-jiyan/one-search-api/internal/query"
	"github.com/Wu-jiyan/one-search-api/internal/rank"
)

// Source is one configured provider plus its runtime protections.
type Source struct {
	Name    string
	Type    string
	Weight  float64
	Timeout time.Duration
	Prov    provider.Provider
	Bucket  *limit.Bucket
	Breaker *limit.Breaker
}

// ProviderState is a health snapshot for /healthz.
type ProviderState struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	State string `json:"state"`
}

// Service executes searches.
type Service struct {
	cfg        config.SearchConfig
	sources    []*Source
	fetch      *fetchsvc.Service
	metrics    *obs.Metrics
	logger     *slog.Logger
	cache      *cache.Cache[[]httpx.SearchResult]
	flight     *cache.Group[[]httpx.SearchResult]
	contentSem *limit.Semaphore
}

// New builds a search service.
func New(cfg *config.Config, sources []*Source, fetch *fetchsvc.Service, metrics *obs.Metrics, logger *slog.Logger) *Service {
	return &Service{
		cfg:        cfg.Search,
		sources:    sources,
		fetch:      fetch,
		metrics:    metrics,
		logger:     logger,
		cache:      cache.New[[]httpx.SearchResult](cfg.Cache.SearchMaxEntries, cfg.Cache.SearchTTL.D()),
		flight:     &cache.Group[[]httpx.SearchResult]{},
		contentSem: limit.NewSemaphore(cfg.Search.ContentFetchConcurrency),
	}
}

// ProviderStates returns the current provider health snapshot.
func (s *Service) ProviderStates() []ProviderState {
	out := make([]ProviderState, 0, len(s.sources))
	for _, src := range s.sources {
		out = append(out, ProviderState{Name: src.Name, Type: src.Type, State: src.Breaker.State().String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// providerNames returns a stable string of enabled providers (cache key input).
func (s *Service) providerNames(allowed map[string]bool) string {
	names := make([]string, 0, len(s.sources))
	for _, src := range s.sources {
		if allowed != nil && !allowed[src.Name] {
			continue
		}
		names = append(names, src.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// Search runs the full pipeline and returns at most count results.
func (s *Service) Search(ctx context.Context, q query.Query, count int, needContent bool, allowed map[string]bool, meter fetchsvc.Meter) ([]httpx.SearchResult, error) {
	if count <= 0 {
		count = s.cfg.DefaultCount
	}
	if count > s.cfg.MaxCount {
		count = s.cfg.MaxCount
	}

	key := q.Fingerprint() + "|n=" + itoa(count) + "|c=" + boolStr(needContent) + "|p=" + s.providerNames(allowed)
	if cached, ok := s.cache.Get(key); ok {
		s.metrics.Inc("onesearch_cache_hits_total", "kind", "search")
		if len(cached) > count {
			cached = cached[:count]
		}
		return cached, nil
	}

	results, err := cache.Do(s.flight, key, func() ([]httpx.SearchResult, error) {
		return s.run(ctx, q, count, needContent, allowed, meter)
	})
	if err != nil {
		return nil, err
	}
	// Do not cache results produced while the caller's context was already done.
	if ctx.Err() == nil {
		s.cache.Set(key, results)
	}
	return results, nil
}

func (s *Service) run(ctx context.Context, q query.Query, count int, needContent bool, allowed map[string]bool, meter fetchsvc.Meter) ([]httpx.SearchResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, s.cfg.OverallTimeout.D())
	defer cancel()

	batches := s.fanOut(runCtx, q, count, allowed)
	raw := flattenBatches(s.dropStaleBatches(q, batches))
	raw = s.filterOffTopic(q, raw)

	if len(raw) == 0 && runCtx.Err() != nil {
		return nil, httpx.UpstreamTimeout("all search providers timed out")
	}

	weights := make(map[string]float64, len(s.sources))
	for _, src := range s.sources {
		weights[src.Name] = src.Weight
	}
	items := rank.Fuse(raw, q, rank.Options{
		Weights:        weights,
		PerDomainLimit: s.cfg.PerDomainLimit,
		Now:            time.Now(),
	})
	if len(items) > count {
		items = items[:count]
	}

	pages := s.fetchContents(ctx, items, needContent, meter)

	out := make([]httpx.SearchResult, 0, len(items))
	for i, it := range items {
		out = append(out, s.buildResult(it, pages[i]))
	}

	s.metrics.Add("onesearch_results_returned_total", float64(len(out)))
	return out, nil
}

// providerBatch is one provider's complete result set.
type providerBatch struct {
	name    string
	results []provider.Result
}

func flattenBatches(batches []providerBatch) []provider.Result {
	n := 0
	for _, b := range batches {
		n += len(b.results)
	}
	out := make([]provider.Result, 0, n)
	for _, b := range batches {
		out = append(out, b.results...)
	}
	return out
}

// batchRelevance is the fraction of results that mention at least one query term.
func batchRelevance(results []provider.Result, terms []string) float64 {
	if len(results) == 0 || len(terms) == 0 {
		return 0
	}
	hits := 0
	for _, r := range results {
		text := strings.ToLower(r.Title + " " + r.Snippet)
		for _, t := range terms {
			if strings.Contains(text, t) {
				hits++
				break
			}
		}
	}
	return float64(hits) / float64(len(results))
}

// dropStaleBatches discards provider result sets that are clearly unrelated to
// the query while another provider returned an on-topic set. Bing occasionally
// replays another query's cached page from an edge node (seen live: Go queries
// returning Vietnamese-food pages), and this keeps that noise out of ranking.
func (s *Service) dropStaleBatches(q query.Query, batches []providerBatch) []providerBatch {
	terms := q.Terms()
	if len(terms) == 0 || len(batches) < 2 {
		return batches
	}
	scores := make([]float64, len(batches))
	best := 0.0
	for i, b := range batches {
		scores[i] = batchRelevance(b.results, terms)
		if scores[i] > best {
			best = scores[i]
		}
	}
	// Only act when at least one source is clearly on-topic.
	if best < 0.5 {
		return batches
	}
	kept := make([]providerBatch, 0, len(batches))
	for i, b := range batches {
		if scores[i] < 0.2 {
			s.metrics.Inc("onesearch_provider_requests_total", "provider", b.name, "outcome", "stale_dropped")
			s.logger.Debug("dropping off-topic provider batch",
				"provider", b.name, "relevance", scores[i], "best", best)
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == 0 {
		return batches
	}
	return kept
}

// filterOffTopic is the single-provider safety net: if every batch looks off,
// keep only items that mention at least one query term (unless nothing matches,
// in which case the original set is returned rather than an empty page).
func (s *Service) filterOffTopic(q query.Query, raw []provider.Result) []provider.Result {
	terms := q.Terms()
	if len(terms) < 2 || len(raw) == 0 {
		return raw
	}
	if batchRelevance(raw, terms) >= 0.2 {
		return raw
	}
	kept := make([]provider.Result, 0, len(raw))
	for _, r := range raw {
		text := strings.ToLower(r.Title + " " + r.Snippet)
		for _, t := range terms {
			if strings.Contains(text, t) {
				kept = append(kept, r)
				break
			}
		}
	}
	if len(kept) == 0 {
		return raw
	}
	s.logger.Debug("filtered off-topic results", "kept", len(kept), "total", len(raw))
	s.metrics.Add("onesearch_results_filtered_total", float64(len(raw)-len(kept)))
	return kept
}

func (s *Service) fanOut(ctx context.Context, q query.Query, count int, allowed map[string]bool) []providerBatch {
	type outcome struct {
		name string
		res  []provider.Result
		err  error
	}

	srcs := make([]*Source, 0, len(s.sources))
	for _, src := range s.sources {
		if allowed != nil && !allowed[src.Name] {
			s.metrics.Inc("onesearch_provider_requests_total", "provider", src.Name, "outcome", "skipped")
			continue
		}
		if !src.Breaker.Allow() {
			s.metrics.Inc("onesearch_provider_requests_total", "provider", src.Name, "outcome", "breaker_open")
			continue
		}
		if ok, _ := src.Bucket.Allow(); !ok {
			s.metrics.Inc("onesearch_provider_requests_total", "provider", src.Name, "outcome", "rate_limited")
			continue
		}
		srcs = append(srcs, src)
	}
	if len(srcs) == 0 {
		return nil
	}

	fanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan outcome, len(srcs))
	var wg sync.WaitGroup
	for _, src := range srcs {
		wg.Add(1)
		go func(src *Source) {
			defer wg.Done()
			timeout := src.Timeout
			if timeout <= 0 {
				timeout = s.cfg.ProviderTimeout.D()
			}
			pctx, pcancel := context.WithTimeout(fanCtx, timeout)
			defer pcancel()

			start := time.Now()
			res, err := src.Prov.Search(pctx, q)
			s.metrics.ObserveDuration("onesearch_provider_duration_seconds", time.Since(start), "provider", src.Name)

			switch {
			case err == nil:
				src.Breaker.Success()
				s.metrics.Inc("onesearch_provider_requests_total", "provider", src.Name, "outcome", "ok")
			case fanCtx.Err() != nil && errors.Is(err, context.Canceled):
				// Cancelled by the hedge: do not penalize the provider.
				s.metrics.Inc("onesearch_provider_requests_total", "provider", src.Name, "outcome", "hedged")
			default:
				src.Breaker.Failure()
				s.metrics.Inc("onesearch_provider_requests_total", "provider", src.Name, "outcome", "error")
				s.logger.Debug("provider failed", "provider", src.Name, "error", err.Error())
			}
			ch <- outcome{name: src.Name, res: res, err: err}
		}(src)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	// Hedge: once enough results are in hand, stop waiting for stragglers after
	// hedge_delay instead of paying the full per-provider timeout.
	threshold := count / 2
	if threshold < 1 {
		threshold = 1
	}
	if threshold > 5 {
		threshold = 5
	}
	hedgeDelay := s.cfg.HedgeDelay.D()
	if hedgeDelay <= 0 {
		hedgeDelay = 800 * time.Millisecond
	}

	var (
		batches    []providerBatch
		totalItems int
		hedgeC     <-chan time.Time
		hedgeStop  func()
		stopOnce   sync.Once
	)
	stopHedge := func() {
		stopOnce.Do(func() {
			if hedgeStop != nil {
				hedgeStop()
			}
		})
	}
	defer stopHedge()

	pending := len(srcs)
	for pending > 0 {
		select {
		case o, ok := <-ch:
			if !ok {
				pending = 0
				break
			}
			pending--
			if o.err == nil && len(o.res) > 0 {
				batches = append(batches, providerBatch{name: o.name, results: o.res})
				totalItems += len(o.res)
				if hedgeC == nil && totalItems >= threshold {
					t := time.NewTimer(hedgeDelay)
					hedgeC = t.C
					hedgeStop = func() { t.Stop() }
				}
			}
		case <-hedgeC:
			cancel()
			pending = 0
		case <-ctx.Done():
			cancel()
			pending = 0
		}
	}
	return batches
}

// fetchContents fetches page bodies for the first need_content_max_results
// items when need_content is set. Failed fetches simply leave nil.
func (s *Service) fetchContents(ctx context.Context, items []rank.Item, needContent bool, meter fetchsvc.Meter) []*extract.Page {
	pages := make([]*extract.Page, len(items))
	if !needContent || s.fetch == nil {
		return pages
	}
	limitN := s.cfg.NeedContentMaxResults
	if limitN > len(items) {
		limitN = len(items)
	}
	var wg sync.WaitGroup
	for i := 0; i < limitN; i++ {
		if ctx.Err() != nil {
			break
		}
		if !s.contentSem.TryAcquire() {
			// Wait for a slot (bounded by ctx) to keep latency predictable.
			if err := s.contentSem.Acquire(ctx); err != nil {
				break
			}
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer s.contentSem.Release()
			fctx, cancel := context.WithTimeout(ctx, s.cfg.ContentFetchTimeout.D())
			defer cancel()
			page, err := s.fetch.Page(fctx, meter, items[i].URL)
			if err != nil {
				s.metrics.Inc("onesearch_content_fetch_total", "result", "error")
				return
			}
			s.metrics.Inc("onesearch_content_fetch_total", "result", "ok")
			pages[i] = page
		}(i)
	}
	wg.Wait()
	return pages
}

func (s *Service) buildResult(it rank.Item, page *extract.Page) httpx.SearchResult {
	var content string
	if page != nil {
		content = page.Content
		if strings.TrimSpace(content) == "" {
			content = page.Text
		}
	}

	snippets := it.Snippets
	if len(snippets) == 0 && it.Snippet != "" {
		snippets = []string{it.Snippet}
	}

	date := it.Date
	if date.IsZero() && page != nil && page.Published != nil {
		date = *page.Published
	}
	fetchedSite := it.SiteName
	if page != nil && page.SiteName != "" {
		fetchedSite = page.SiteName
	}

	return httpx.SearchResult{
		RerankScore: it.Score,
		Title:       it.Title,
		URL:         it.URL,
		Snippet:     enrich.Snippet(it.Snippet),
		Summary:     enrich.Summary(content, snippets),
		Content:     truncateRunes(content, 200_000),
		SiteName:    enrich.SiteName(it.Host, fetchedSite),
		SiteIcon:    enrich.Icon(it.Host, s.cfg.SiteIconTemplate),
		Date:        httpx.NewDate(date),
		Authority:   it.Authority,
	}
}

func truncateRunes(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max])
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

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
