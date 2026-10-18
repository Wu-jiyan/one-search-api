package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wujiyan/one-search-api/internal/query"
)

type searxng struct {
	name    string
	base    string
	token   string
	headers map[string]string
	region  string
	client  *http.Client
}

func (s *searxng) Name() string { return s.name }
func (s *searxng) Type() string { return "searxng" }

type searxngResponse struct {
	Results []struct {
		URL           string   `json:"url"`
		Title         string   `json:"title"`
		Content       string   `json:"content"`
		PublishedDate string   `json:"publishedDate"`
		Score         float64  `json:"score"`
		Engines       []string `json:"engines"`
		Category      string   `json:"category"`
	} `json:"results"`
	UnresponsiveEngines [][]string `json:"unresponsive_engines"`
}

// Search pages through SearXNG until it has enough results, the upstream stops
// yielding new URLs, or maxSearxPages is reached. A single SearXNG page returns
// roughly 10-20 hits, so pagination is what makes count>20 meaningful.
func (s *searxng) Search(ctx context.Context, q query.Query) ([]Result, error) {
	want := q.Count
	if want <= 0 {
		want = 10
	}
	const maxSearxPages = 5

	var out []Result
	seen := map[string]bool{}
	var lastErr error
	for page := 1; page <= maxSearxPages && len(out) < want; page++ {
		res, err := s.page(ctx, q, page)
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
			break // upstream has nothing new; stop paging
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

func (s *searxng) page(ctx context.Context, q query.Query, page int) ([]Result, error) {
	params := url.Values{}
	params.Set("q", q.EngineString())
	params.Set("format", "json")
	params.Set("pageno", strconv.Itoa(page))
	params.Set("safesearch", "0")
	if len(q.Langs) > 0 {
		params.Set("language", q.Langs[0])
	}
	if tr := q.TimeRange(nowUTC()); tr != "" {
		params.Set("time_range", tr)
	}

	endpoint := strings.TrimRight(s.base, "/") + "/search?" + params.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.token != "" {
		req.Header.Set("X-Token", s.token)
	}
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}

	var payload searxngResponse
	if err := doJSON(ctx, s.client, req, &payload); err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}

	out := make([]Result, 0, len(payload.Results))
	for _, r := range payload.Results {
		u, canon := NormalizeURL(r.URL)
		if u == nil {
			continue
		}
		if !q.AllowsURL(u) {
			continue
		}
		res := Result{
			Title:    cleanText(r.Title),
			URL:      canon,
			Snippet:  cleanText(r.Content),
			Date:     ParseDate(r.PublishedDate),
			Engine:   s.name,
			Rank:     len(out) + 1,
			RawScore: r.Score,
		}
		if res.Title == "" {
			continue
		}
		out = append(out, res)
	}

	if len(out) == 0 && len(payload.UnresponsiveEngines) > 0 {
		return nil, fmt.Errorf("searxng: no results, unresponsive engines: %v", payload.UnresponsiveEngines)
	}
	return out, nil
}
