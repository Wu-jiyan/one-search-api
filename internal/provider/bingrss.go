package provider

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"

	"github.com/wujiyan/one-search-api/internal/query"
)

// bingRSS uses Bing's RSS rendering of the same result set as the HTML page.
// It is much lighter (about 5 KB vs 100 KB) and is the default Bing channel.
type bingRSS struct {
	name string
	sess *bingSession
}

func (b *bingRSS) Name() string { return b.name }
func (b *bingRSS) Type() string { return "bingrss" }

// Warm pre-warms the browser session (called at startup).
func (b *bingRSS) Warm(ctx context.Context) { b.sess.Warm(ctx) }

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

// Search retries the RSS channel once with a freshly generated browser identity,
// then falls back to parsing the HTML page. Bing soft-blocks bot-like requests
// with HTTP 200 and a valid but empty <channel>, so an empty parse is treated as
// a retryable condition rather than "no results".
func (b *bingRSS) Search(ctx context.Context, q query.Query) ([]Result, error) {
	var lastErr error
	var lastResults []Result
	more := func(first int) ([]Result, error) { return b.oncePage(ctx, q, first) }
	for attempt := 0; attempt < 2; attempt++ {
		res, err := b.once(ctx, q)
		if err != nil {
			lastErr = err
		} else if len(res) > 0 {
			lastResults = res
			if !suspiciousResults(res, q) {
				return paginateBing(ctx, q, res, more), nil
			}
			debugf("bingrss attempt=%d results=%d look off-topic, retrying", attempt+1, len(res))
		}
		debugf("bingrss attempt=%d results=%d err=%v", attempt+1, len(res), err)
		if attempt == 0 {
			b.sess.invalidate()
			b.sess.waitRetry(ctx, attempt)
			if ctx.Err() != nil {
				break
			}
		}
	}

	res, herr := bingHTMLResults(ctx, b.sess, q, b.name)
	if herr == nil && len(res) > 0 {
		if !suspiciousResults(res, q) {
			debugf("bingrss recovered %d results via HTML fallback", len(res))
			// The fallback renderer can paginate even when the RSS feed is
			// soft-blocked, so keep filling the requested count.
			return paginateBing(ctx, q, res, func(first int) ([]Result, error) {
				return bingHTMLResultsPage(ctx, b.sess, q, b.name, first)
			}), nil
		}
		if len(lastResults) == 0 {
			lastResults = res
		}
	}
	if herr != nil {
		debugf("bingrss HTML fallback failed: %v", herr)
	}
	if len(lastResults) > 0 {
		return lastResults, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if herr != nil {
		return nil, herr
	}
	return nil, nil
}

func (b *bingRSS) once(ctx context.Context, q query.Query) ([]Result, error) {
	return b.oncePage(ctx, q, 0)
}

// oncePage fetches one RSS page at the given &first= offset (0 for the first
// page).
func (b *bingRSS) oncePage(ctx context.Context, q query.Query, first int) ([]Result, error) {
	target := b.sess.searchURLPage(q, "", true, first)
	resp, err := b.sess.get(ctx, target, "application/rss+xml, application/xml;q=0.9, */*;q=0.1")
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
	trimmed := trimSpaceBytes(body)
	if !hasPrefix(trimmed, "<?xml") && !hasPrefix(trimmed, "<rss") {
		return nil, fmt.Errorf("bingrss: unexpected content %q", resp.Header.Get("Content-Type"))
	}

	var doc rssDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("bingrss: decode xml: %w", err)
	}
	b.sess.noteQuery(q.EngineString())
	debugf("bingrss url=%q bytes=%d structItems=%d", target, len(body), len(doc.Channel.Items))

	return rssResults(&doc, q, b.name), nil
}

// rssResults converts decoded RSS items into provider results.
func rssResults(doc *rssDocument, q query.Query, engine string) []Result {
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
			Engine:  engine,
			Rank:    len(out) + 1,
		})
	}
	return out
}

func trimSpaceBytes(b []byte) string {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
		end--
	}
	return string(b[start:end])
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
