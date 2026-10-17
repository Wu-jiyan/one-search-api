// Package rank normalizes, de-duplicates, fuses and scores search results.
package rank

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wujiyan/one-search-api/internal/enrich"
	"github.com/wujiyan/one-search-api/internal/provider"
	"github.com/wujiyan/one-search-api/internal/query"
	"github.com/wujiyan/one-search-api/internal/siteinfo"
)

// Item is one fused search result with its computed score.
type Item struct {
	provider.Result
	Host      string
	Sources   []string
	Snippets  []string
	Authority int
	Score     float64
	rrf       float64
}

// Options tunes fusion.
type Options struct {
	Weights        map[string]float64
	PerDomainLimit int
	Now            time.Time
	RRFK           float64
}

const defaultRRFK = 60.0

// Fuse merges provider results, applies query filters, scores them and enforces
// per-domain diversity. The returned slice is ordered best-first; items that
// exceeded the per-domain limit are appended at the end so callers can still
// use them when too few results remain.
func Fuse(results []provider.Result, q query.Query, opts Options) []Item {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.PerDomainLimit <= 0 {
		opts.PerDomainLimit = 3
	}
	k := opts.RRFK
	if k <= 0 {
		k = defaultRRFK
	}

	items := make([]Item, 0, len(results))
	byURL := make(map[string]int, len(results))
	byTitle := make(map[string]int, len(results))

	for _, r := range results {
		u, canon := provider.NormalizeURL(r.URL)
		if u == nil {
			continue
		}
		if !q.AllowsURL(u) || !q.AllowsDate(r.Date) {
			continue
		}
		if q.ExcludesText(r.Title + " " + r.Snippet) {
			continue
		}
		title := enrich.Title(r.Title)
		if title == "" {
			continue
		}

		urlKey := provider.HostKey(u)
		titleKey := strings.ToLower(u.Hostname()) + "|" + enrich.NormalizeTitle(title)

		idx, ok := byURL[urlKey]
		if !ok {
			if ti, found := byTitle[titleKey]; found {
				idx, ok = ti, true
			}
		}

		weight := 1.0
		if w, found := opts.Weights[r.Engine]; found && w > 0 {
			weight = w
		}
		contrib := weight / (k + float64(max(r.Rank, 1)))

		if ok {
			it := &items[idx]
			it.rrf += contrib
			it.Sources = appendSource(it.Sources, r.Engine)
			if len(r.Snippet) > len(it.Snippet) {
				it.Snippet = r.Snippet
			}
			if r.Snippet != "" {
				it.Snippets = appendSource(it.Snippets, r.Snippet)
			}
			if it.Date.IsZero() && !r.Date.IsZero() {
				it.Date = r.Date
			}
			if it.SiteName == "" && r.SiteName != "" {
				it.SiteName = r.SiteName
			}
			if r.RawScore > it.RawScore {
				it.RawScore = r.RawScore
			}
			continue
		}

		it := Item{
			Result: provider.Result{
				Title:    title,
				URL:      canon,
				Snippet:  r.Snippet,
				Date:     r.Date,
				Engine:   r.Engine,
				Rank:     r.Rank,
				RawScore: r.RawScore,
				SiteName: r.SiteName,
			},
			Host:    strings.ToLower(u.Hostname()),
			Sources: []string{r.Engine},
			rrf:     contrib,
		}
		if r.Snippet != "" {
			it.Snippets = []string{r.Snippet}
		}
		items = append(items, it)
		idx = len(items) - 1
		byURL[urlKey] = idx
		byTitle[titleKey] = idx
	}

	if len(items) == 0 {
		return nil
	}

	var maxRRF float64
	for i := range items {
		if items[i].rrf > maxRRF {
			maxRRF = items[i].rrf
		}
	}
	if maxRRF <= 0 {
		maxRRF = 1
	}

	terms := q.Terms()
	for i := range items {
		items[i].Authority = siteinfo.Authority(items[i].Host)
		items[i].Score = score(&items[i], q, terms, maxRRF, opts.Now)
	}

	sort.SliceStable(items, func(a, b int) bool {
		if items[a].Score != items[b].Score {
			return items[a].Score > items[b].Score
		}
		return items[a].rrf > items[b].rrf
	})

	// Per-domain diversity: keep the first N per registrable domain, defer the rest.
	perDomain := map[string]int{}
	limited := make([]Item, 0, len(items))
	deferred := make([]Item, 0, len(items))
	for _, it := range items {
		d := siteinfo.RegistrableDomain(it.Host)
		if d == "" {
			d = it.Host
		}
		if perDomain[d] >= opts.PerDomainLimit {
			deferred = append(deferred, it)
			continue
		}
		perDomain[d]++
		limited = append(limited, it)
	}
	return append(limited, deferred...)
}

func score(it *Item, q query.Query, terms []string, maxRRF float64, now time.Time) float64 {
	rel := math.Pow(it.rrf/maxRRF, 0.6)

	authN := 0.5
	if it.Authority >= 0 {
		authN = float64(it.Authority) / 4.0
	}

	fresh := 0.5
	if !it.Date.IsZero() {
		ageDays := now.Sub(it.Date).Hours() / 24
		if ageDays < 0 {
			ageDays = 0
		}
		fresh = math.Exp(-ageDays / 180.0)
	}

	titleLower := strings.ToLower(it.Title)
	titleHits := 0.0
	if len(terms) > 0 {
		hit := 0
		for _, t := range terms {
			if strings.Contains(titleLower, t) {
				hit++
			}
		}
		titleHits = float64(hit) / float64(len(terms))
	}
	phraseHit := 0.0
	for _, p := range q.Phrases {
		if strings.Contains(titleLower, strings.ToLower(p)) {
			phraseHit = 1
			break
		}
	}
	siteHit := 0.0
	if len(q.Sites) > 0 {
		siteHit = 1
	}

	raw := 0.62*rel + 0.23*authN + 0.15*fresh
	boost := math.Min(0.25, 0.10*titleHits+0.10*phraseHit+0.08*siteHit)
	v := 3 * (raw + boost)
	if v < 0 {
		v = 0
	}
	if v > 3 {
		v = 3
	}
	return math.Round(v*100) / 100
}

func appendSource(sources []string, engine string) []string {
	for _, s := range sources {
		if s == engine {
			return sources
		}
	}
	return append(sources, engine)
}
