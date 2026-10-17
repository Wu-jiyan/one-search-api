package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/wujiyan/one-search-api/internal/query"
)

type duckDuckGo struct {
	name    string
	base    string
	headers map[string]string
	region  string
	client  *http.Client
}

func (d *duckDuckGo) Name() string { return d.name }
func (d *duckDuckGo) Type() string { return "duckduckgo" }

func (d *duckDuckGo) Search(ctx context.Context, q query.Query) ([]Result, error) {
	form := url.Values{}
	form.Set("q", q.EngineString())
	if kl := ddgRegion(q.Langs, d.region); kl != "" {
		form.Set("kl", kl)
	}
	if df := ddgDateFilter(q.TimeRange(nowUTC())); df != "" {
		form.Set("df", df)
	}

	endpoint := strings.TrimRight(d.base, "/") + "/html/"
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://html.duckduckgo.com/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	for k, v := range d.headers {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo: upstream status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("duckduckgo: parse html: %w", err)
	}
	if doc.Find(".anomaly-modal, #challenge-form").Length() > 0 {
		return nil, fmt.Errorf("duckduckgo: bot challenge encountered")
	}

	out := make([]Result, 0, 16)
	doc.Find(".result").EachWithBreak(func(_ int, sel *goquery.Selection) bool {
		link := sel.Find(".result__a").First()
		href, ok := link.Attr("href")
		if !ok || href == "" {
			return true
		}
		raw := unwrapDDG(href)
		u, canon := NormalizeURL(raw)
		if u == nil || !q.AllowsURL(u) {
			return true
		}
		title := cleanText(link.Text())
		if title == "" {
			return true
		}
		snippet := cleanText(sel.Find(".result__snippet").First().Text())
		out = append(out, Result{
			Title:   title,
			URL:     canon,
			Snippet: snippet,
			Engine:  d.name,
			Rank:    len(out) + 1,
		})
		return true
	})
	return out, nil
}

// unwrapDDG resolves duckduckgo.com/l/?uddg=... redirect links.
func unwrapDDG(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if strings.Contains(strings.ToLower(u.Host), "duckduckgo.com") {
		if target := u.Query().Get("uddg"); target != "" {
			if decoded, err := url.QueryUnescape(target); err == nil {
				return decoded
			}
			return target
		}
	}
	return href
}

func ddgDateFilter(timeRange string) string {
	switch timeRange {
	case "day":
		return "d"
	case "week":
		return "w"
	case "month":
		return "m"
	case "year":
		return "y"
	default:
		return ""
	}
}

func ddgRegion(langs []string, fallback string) string {
	if fallback != "" {
		return fallback
	}
	if len(langs) == 0 {
		return ""
	}
	switch strings.ToLower(strings.Split(langs[0], "-")[0]) {
	case "zh":
		return "cn-zh"
	case "en":
		return "us-en"
	case "ja":
		return "jp-jp"
	case "ko":
		return "kr-kr"
	case "de":
		return "de-de"
	case "fr":
		return "fr-fr"
	case "ru":
		return "ru-ru"
	case "es":
		return "es-es"
	default:
		return ""
	}
}
