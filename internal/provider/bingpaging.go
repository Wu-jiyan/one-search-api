package provider

import (
	"context"

	"github.com/Wu-jiyan/one-search-api/internal/query"
)

// bingMaxPages bounds pagination so a single provider call stays predictable.
const bingMaxPages = 5

// paginateBing walks Bing's &first= offset until the requested count is met, the
// endpoint stops yielding new URLs, or bingMaxPages is reached.
//
// The behavior is adaptive on purpose: the international endpoint honours the
// offset (verified from a Hong Kong host: pages 2 and 3 return new results for
// both the HTML and RSS renderings), while cn.bing.com ignores it and repeats
// page 1. A repeated page adds zero new URLs, so the loop stops after one extra
// request in the domestic case.
func paginateBing(
	ctx context.Context,
	q query.Query,
	first []Result,
	fetchPage func(first int) ([]Result, error),
) []Result {
	if len(first) == 0 || q.Count <= len(first) {
		return first
	}

	out := first
	seen := make(map[string]bool, len(first))
	for _, r := range first {
		seen[r.URL] = true
	}
	// raw counts what the endpoint actually returned, including duplicates
	// across pages; &first= is an offset into that sequence, not into deduped
	// output.
	raw := len(first)

	for page := 2; page <= bingMaxPages && len(out) < q.Count; page++ {
		if ctx.Err() != nil {
			break
		}
		res, err := fetchPage(raw + 1)
		if err != nil || len(res) == 0 {
			break
		}
		raw += len(res)
		added := 0
		for _, r := range res {
			if seen[r.URL] {
				continue
			}
			seen[r.URL] = true
			r.Rank = len(out) + 1
			out = append(out, r)
			added++
		}
		debugf("bing pagination page=%d offset=%d got=%d added=%d total=%d",
			page, raw, len(res), added, len(out))
		if added == 0 {
			break
		}
	}
	return out
}
