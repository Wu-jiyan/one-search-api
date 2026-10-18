package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/Wu-jiyan/one-search-api/internal/query"
)

// bingHTML scrapes the regular Bing web results page. It shares Bing's backend
// result set with the RSS channel but produces richer markup (structured
// captions and dates), at the cost of a ~100 KB response.
type bingHTML struct {
	name string
	sess *bingSession
}

func (b *bingHTML) Name() string { return b.name }
func (b *bingHTML) Type() string { return "binghtml" }

// Warm pre-warms the browser session (called at startup).
func (b *bingHTML) Warm(ctx context.Context) { b.sess.Warm(ctx) }

// Search retries once with a rotated browser identity when Bing serves a bot
// challenge page instead of results, then pages through &first= while the
// endpoint keeps yielding new URLs.
func (b *bingHTML) Search(ctx context.Context, q query.Query) ([]Result, error) {
	var lastErr error
	var lastResults []Result
	more := func(first int) ([]Result, error) {
		return bingHTMLResultsPage(ctx, b.sess, q, b.name, first)
	}
	for attempt := 0; attempt < 2; attempt++ {
		res, err := bingHTMLResults(ctx, b.sess, q, b.name)
		if err == nil {
			if len(res) == 0 || !suspiciousResults(res, q) {
				return paginateBing(ctx, q, res, more), nil
			}
			lastResults = res
			debugf("binghtml attempt=%d results=%d look off-topic, retrying", attempt+1, len(res))
		} else {
			lastErr = err
		}
		if attempt == 0 {
			b.sess.invalidate()
			b.sess.waitRetry(ctx, attempt)
			if ctx.Err() != nil {
				break
			}
		}
	}
	if len(lastResults) > 0 {
		// These results already failed the relevance check; paging through more
		// of the same degraded response would only amplify junk.
		return lastResults, nil
	}
	return nil, lastErr
}

// bingHTMLResults fetches and parses one Bing HTML results page.
func bingHTMLResults(ctx context.Context, sess *bingSession, q query.Query, engine string) ([]Result, error) {
	return bingHTMLResultsPage(ctx, sess, q, engine, 0)
}

// bingHTMLResultsPage fetches one HTML page at the given &first= offset (0 for
// the first page).
func bingHTMLResultsPage(ctx context.Context, sess *bingSession, q query.Query, engine string, first int) ([]Result, error) {
	target := sess.searchURLPage(q, "", false, first)
	resp, err := sess.get(ctx, target, "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	if err != nil {
		return nil, fmt.Errorf("binghtml: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("binghtml: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binghtml: upstream status %d", resp.StatusCode)
	}
	sess.noteQuery(q.EngineString())
	debugf("binghtml url=%q bytes=%d", target, len(body))
	return parseBingHTML(body, engine, q)
}

var (
	reBingLeadingDate = regexp.MustCompile(`^\s*(\d{4}年\d{1,2}月\d{1,2}日|\d{4}-\d{2}-\d{2}|\d{1,2} [A-Za-z]{3,9} \d{4})`)
	reBingNoResults   = regexp.MustCompile(`(?i)(there are no results|没有与此相关的结果|找不到与此相关的结果)`)
)

func parseBingHTML(body []byte, engine string, q query.Query) ([]Result, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("binghtml: parse html: %w", err)
	}

	out := make([]Result, 0, 12)
	doc.Find("li.b_algo").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		if len(out) >= 50 {
			return false
		}
		link := sel.Find("h2 a").First()
		if link.Length() == 0 {
			link = sel.Find("a").First()
		}
		href, ok := link.Attr("href")
		if !ok || strings.TrimSpace(href) == "" {
			return true
		}
		target := unwrapBingRedirect(href)
		u, canon := NormalizeURL(target)
		if u == nil || !q.AllowsURL(u) {
			return true
		}
		title := cleanText(link.Text())
		if title == "" {
			return true
		}

		snippet := cleanText(sel.Find(".b_caption p").First().Text())
		if snippet == "" {
			snippet = cleanText(sel.Find(".b_lineclamp1, .b_lineclamp2, .b_lineclamp3, .b_lineclamp4").First().Text())
		}
		if snippet == "" {
			snippet = cleanText(sel.Find("p").First().Text())
		}

		date := ParseDate(cleanText(sel.Find(".news_dt").First().Text()))
		if date.IsZero() {
			date = leadingBingDate(snippet)
		}

		out = append(out, Result{
			Title:   title,
			URL:     canon,
			Snippet: snippet,
			Date:    date,
			Engine:  engine,
			Rank:    len(out) + 1,
		})
		return true
	})

	if len(out) > 0 {
		return out, nil
	}
	if doc.Find("#b_no").Length() > 0 || reBingNoResults.Match(body) {
		return nil, nil
	}
	return nil, fmt.Errorf("binghtml: no results parsed (possible bot challenge)")
}

// unwrapBingRedirect resolves bing.com/ck/a?...&u=a1<base64url> links.
func unwrapBingRedirect(href string) string {
	if strings.HasPrefix(href, "/") {
		href = "https://www.bing.com" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), "bing.com") {
		return href
	}
	if !strings.HasSuffix(u.Path, "/ck/a") {
		return href
	}
	raw := strings.TrimPrefix(u.Query().Get("u"), "a1")
	if raw == "" {
		return href
	}
	if decoded, derr := base64.RawURLEncoding.DecodeString(raw); derr == nil {
		target := string(decoded)
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			return target
		}
	}
	return href
}

// leadingBingDate extracts a date that Bing sometimes prefixes to the snippet.
func leadingBingDate(s string) time.Time {
	m := reBingLeadingDate.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}
	}
	return ParseDate(m[1])
}
