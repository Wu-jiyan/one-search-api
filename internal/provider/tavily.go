package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wu-jiyan/one-search-api/internal/query"
)

type tavily struct {
	name    string
	apiKey  string
	base    string
	headers map[string]string
	region  string
	client  *http.Client
}

func (t *tavily) Name() string { return t.name }
func (t *tavily) Type() string { return "tavily" }

type tavilyRequest struct {
	Query             string `json:"query"`
	MaxResults        int    `json:"max_results"`
	SearchDepth       string `json:"search_depth"`
	Topic             string `json:"topic,omitempty"`
	IncludeAnswer     bool   `json:"include_answer"`
	IncludeRawContent bool   `json:"include_raw_content"`
}

type tavilyResponse struct {
	Results []struct {
		Title         string  `json:"title"`
		URL           string  `json:"url"`
		Content       string  `json:"content"`
		Score         float64 `json:"score"`
		PublishedDate string  `json:"published_date"`
	} `json:"results"`
}

func (t *tavily) Search(ctx context.Context, q query.Query) ([]Result, error) {
	count := q.Count
	if count <= 0 {
		count = 10
	}
	if count > 20 {
		count = 20
	}
	body := tavilyRequest{
		Query:             q.TextOnly(),
		MaxResults:        count,
		SearchDepth:       "basic",
		IncludeAnswer:     false,
		IncludeRawContent: false,
	}
	if body.Query == "" {
		body.Query = q.EngineString()
	}
	if q.After != nil || q.Before != nil {
		body.Topic = "general"
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(t.base, "/") + "/search"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}

	var payload tavilyResponse
	if err := doJSON(ctx, t.client, req, &payload); err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}

	out := make([]Result, 0, len(payload.Results))
	for _, r := range payload.Results {
		u, canon := NormalizeURL(r.URL)
		if u == nil || !q.AllowsURL(u) {
			continue
		}
		title := cleanText(r.Title)
		if title == "" {
			continue
		}
		out = append(out, Result{
			Title:    title,
			URL:      canon,
			Snippet:  cleanText(r.Content),
			Date:     ParseDate(r.PublishedDate),
			Engine:   t.name,
			Rank:     len(out) + 1,
			RawScore: r.Score,
		})
	}
	return out, nil
}
