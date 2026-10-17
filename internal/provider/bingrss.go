package provider

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wujiyan/one-search-api/internal/query"
)

type bingRSS struct {
	name    string
	base    string
	headers map[string]string
	region  string
	client  *http.Client
}

func (b *bingRSS) Name() string { return b.name }
func (b *bingRSS) Type() string { return "bingrss" }

type rssDocument struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
		} `xml:"item"`
	} `xml:"channel"`
}

func (b *bingRSS) Search(ctx context.Context, q query.Query) ([]Result, error) {
	params := url.Values{}
	params.Set("q", q.EngineString())
	params.Set("format", "rss")
	if q.Count > 0 {
		params.Set("count", strconv.Itoa(min(q.Count, 30)))
	}
	if len(q.Langs) > 0 {
		params.Set("setlang", q.Langs[0])
	}
	if b.region != "" {
		params.Set("cc", b.region)
	}

	endpoint := strings.TrimRight(b.base, "/") + "/search?" + params.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9, */*;q=0.1")
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}

	resp, err := b.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("bingrss: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("bingrss: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bingrss: upstream status %d", resp.StatusCode)
	}
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "<?xml") && !strings.HasPrefix(trimmed, "<rss") {
		// Bing sometimes answers with an HTML error/interstitial page.
		return nil, fmt.Errorf("bingrss: unexpected content type %q", resp.Header.Get("Content-Type"))
	}

	var doc rssDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("bingrss: decode xml: %w", err)
	}

	out := make([]Result, 0, len(doc.Channel.Items))
	for _, item := range doc.Channel.Items {
		u, canon := NormalizeURL(item.Link)
		if u == nil || !q.AllowsURL(u) {
			continue
		}
		title := cleanText(item.Title)
		if title == "" {
			continue
		}
		out = append(out, Result{
			Title:   title,
			URL:     canon,
			Snippet: cleanText(item.Description),
			Date:    ParseDate(item.PubDate),
			Engine:  b.name,
			Rank:    len(out) + 1,
		})
	}
	return out, nil
}
