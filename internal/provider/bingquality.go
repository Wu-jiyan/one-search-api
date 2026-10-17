package provider

import (
	"strings"

	"github.com/wujiyan/one-search-api/internal/query"
)

// resultsRelevance is the fraction of results that mention at least one query
// term. Bing sometimes replays another query's cached page; a low relevance
// score on a non-empty result set is the signal for that.
func resultsRelevance(results []Result, terms []string) float64 {
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

// suspiciousResults reports whether a non-empty result set looks like it was
// served from the wrong query cache. Single-term queries are exempt: semantic
// matches legitimately may not repeat the literal term.
func suspiciousResults(results []Result, q query.Query) bool {
	terms := q.Terms()
	if len(results) == 0 || len(terms) < 2 {
		return false
	}
	return resultsRelevance(results, terms) < 0.2
}
