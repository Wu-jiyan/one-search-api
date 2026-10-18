package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wu-jiyan/one-search-api/internal/query"
)

type brave struct {
	name    string
	apiKey  string
	base    string
	headers map[string]string
	region  string
	client  *http.Client
}

func (b *brave) Name() string { return b.name }
func (b *brave) Type() string { return "brave" }

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			PageAge     string `json:"page_age"`
			Age         string `json:"age"`
			Profile     struct {
				Name string `json:"name"`
			} `json:"profile"`
		} `json:"results"`
	} `json:"web"`
}

// Search pages through Brave's web endpoint. One response carries at most 20
// results and Brave accepts a 0-based page offset, so count>20 needs several
// calls.
func (b *brave) Search(ctx context.Context, q query.Query) ([]Result, error) {
	want := q.Count
	if want <= 0 {
		want = 10
	}

	var out []Result
	seen := map[string]bool{}
	var lastErr error
	// Brave's offset is the page index (0-9) and each page holds at most 20.
	for page := 0; page <= 9 && len(out) < want; page++ {
		size := want - len(out)
		if size > 20 {
			size = 20
		}
		res, err := b.page(ctx, q, size, page)
		if err != nil {
			lastErr = err
			break
		}
		added := 0
		for _, r := range res {
			if seen[r.URL] {
				continue
			}
			seen[r.URL] = true
			out = append(out, r)
			added++
		}
		if added == 0 {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out, nil
}

func (b *brave) page(ctx context.Context, q query.Query, count, offset int) ([]Result, error) {
	params := url.Values{}
	params.Set("q", q.EngineString())
	params.Set("count", strconv.Itoa(count))
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	if len(q.Langs) > 0 {
		params.Set("search_lang", strings.Split(q.Langs[0], "-")[0])
	}
	if b.region != "" {
		params.Set("country", b.region)
	}
	// Brave has no native publish-date range in the free web endpoint; results
	// are post-filtered by AllowsDate in the orchestrator.

	endpoint := strings.TrimRight(b.base, "/") + "/res/v1/web/search?" + params.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.apiKey)
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}

	var payload braveResponse
	if err := doJSON(ctx, b.client, req, &payload); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}

	out := make([]Result, 0, len(payload.Web.Results))
	for _, r := range payload.Web.Results {
		u, canon := NormalizeURL(r.URL)
		if u == nil || !q.AllowsURL(u) {
			continue
		}
		title := cleanText(r.Title)
		if title == "" {
			continue
		}
		date := ParseDate(r.PageAge)
		if date.IsZero() {
			date = ParseDate(r.Age)
		}
		out = append(out, Result{
			Title:    title,
			URL:      canon,
			Snippet:  cleanText(r.Description),
			Date:     date,
			Engine:   b.name,
			Rank:     len(out) + 1,
			SiteName: cleanText(r.Profile.Name),
		})
	}
	return out, nil
}
