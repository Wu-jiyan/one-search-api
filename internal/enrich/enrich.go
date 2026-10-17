// Package enrich turns raw provider fields into DeepSeek-compatible values and
// applies the documented heuristics for snippet/summary/site metadata.
package enrich

import (
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wujiyan/one-search-api/internal/siteinfo"
)

var (
	reImg        = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reHeading    = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s*`)
	reQuote      = regexp.MustCompile(`(?m)^\s{0,3}>\s?`)
	reBullet     = regexp.MustCompile(`(?m)^\s{0,3}([-*+]|\d+[.)])\s+`)
	reCodeFence  = regexp.MustCompile("(?s)```.*?```")
	reInlineCode = regexp.MustCompile("`([^`]*)`")
	reEmphasis   = regexp.MustCompile(`[*_~]{1,3}`)
	reTag        = regexp.MustCompile(`<[^>]*>`)
	reSpaces     = regexp.MustCompile(`\s+`)
	reTableSep   = regexp.MustCompile(`(?m)^\s*\|?[\s:|-]{3,}\|?\s*$`)
)

// MarkdownToText flattens Markdown into a single readable paragraph.
func MarkdownToText(md string) string {
	if md == "" {
		return ""
	}
	s := reCodeFence.ReplaceAllString(md, " ")
	s = reImg.ReplaceAllString(s, "")
	s = reLink.ReplaceAllString(s, "$1")
	s = reTableSep.ReplaceAllString(s, "")
	s = reHeading.ReplaceAllString(s, "")
	s = reQuote.ReplaceAllString(s, "")
	s = reBullet.ReplaceAllString(s, "")
	s = reInlineCode.ReplaceAllString(s, "$1")
	s = reEmphasis.ReplaceAllString(s, "")
	s = reTag.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "|", " ")
	s = strings.ReplaceAll(s, "#", " ")
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// Title cleans a provider title and clips it to 300 runes.
func Title(s string) string {
	s = strings.Join(strings.Fields(reTag.ReplaceAllString(html.UnescapeString(s), " ")), " ")
	return Clip(s, 300)
}

// Snippet returns a 100-200 rune summary suitable for display.
func Snippet(raw string) string {
	text := strings.Join(strings.Fields(reTag.ReplaceAllString(html.UnescapeString(raw), " ")), " ")
	if text == "" {
		return ""
	}
	return clipAtSentence(text, 200, 60)
}

// Summary builds the long summary: from page content when available, else from
// the snippets gathered for the same result.
func Summary(contentMarkdown string, snippets []string) string {
	text := MarkdownToText(contentMarkdown)
	if utf8.RuneCountInString(text) >= 120 {
		return clipAtSentence(text, 800, 400)
	}

	var parts []string
	seen := map[string]bool{}
	if text != "" {
		parts = append(parts, text)
		seen[text] = true
	}
	for _, s := range snippets {
		s = strings.Join(strings.Fields(reTag.ReplaceAllString(html.UnescapeString(s), " ")), " ")
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		parts = append(parts, s)
		if utf8.RuneCountInString(strings.Join(parts, " ")) >= 800 {
			break
		}
	}
	joined := strings.TrimSpace(strings.Join(parts, " "))
	if joined == "" {
		return ""
	}
	return clipAtSentence(joined, 800, 200)
}

// Clip truncates s to at most max runes, appending an ellipsis when cut.
func Clip(s string, max int) string {
	return clipAtSentence(s, max, max)
}

// SiteName resolves site_name: fetched page metadata, then a builtin table,
// then the registrable domain.
func SiteName(host, fetched string) *string {
	if v := strings.TrimSpace(fetched); v != "" {
		return &v
	}
	if v := siteinfo.Name(host); v != "" {
		return &v
	}
	if v := siteinfo.RegistrableDomain(host); v != "" {
		return &v
	}
	return nil
}

// Icon renders site_icon from the configured template.
func Icon(host, template string) *string {
	if v := siteinfo.Icon(host, template); v != "" {
		return &v
	}
	return nil
}

// DatePtr converts a date to the nullable wire form (UTC, no monotonic clock).
func DatePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC().Round(0)
	return &u
}

// NormalizeTitle produces a comparison key: letters, digits and CJK only.
func NormalizeTitle(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// clipAtSentence cuts text to at most max runes, preferring a sentence boundary
// in [min, max], then a whitespace boundary, and appends "…" when cut.
func clipAtSentence(s string, max, min int) string {
	if max <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if min > max {
		min = max
	}
	runes := []rune(s)
	window := string(runes[:max])
	// Prefer a sentence terminator.
	for i := len([]rune(window)) - 1; i >= min; i-- {
		if isSentenceEnd(runes[i]) {
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	// Then a whitespace boundary.
	for i := max - 1; i >= min; i-- {
		if unicode.IsSpace(runes[i]) {
			return strings.TrimSpace(string(runes[:i])) + "…"
		}
	}
	return strings.TrimSpace(window) + "…"
}

func isSentenceEnd(r rune) bool {
	switch r {
	case '。', '！', '？', '.', '!', '?', '；', ';':
		return true
	}
	return false
}
