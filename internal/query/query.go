// Package query parses DeepSeek-style search queries (site:, phrases, -term,
// lang:, after:/before:) into a structured form and rebuilds engine queries.
package query

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrEmpty means the query contained nothing usable.
var ErrEmpty = errors.New("query is empty")

// ErrTooLong means the raw query exceeded the 128-rune limit.
var ErrTooLong = errors.New("query exceeds 128 characters")

// Query is a parsed search query.
type Query struct {
	Raw            string
	Text           string
	Phrases        []string
	ExcludePhrases []string
	ExcludeTerms   []string
	Sites          []string
	ExcludeSites   []string
	Langs          []string
	After          *time.Time
	Before         *time.Time
	Count          int
}

type token struct {
	text    string
	quoted  bool
	negated bool
}

// Parse validates and parses a raw query string.
func Parse(raw string) (Query, error) {
	q := Query{Raw: raw}
	if strings.TrimSpace(raw) == "" {
		return q, ErrEmpty
	}
	if utf8.RuneCountInString(raw) > 128 {
		return q, ErrTooLong
	}

	var textTerms []string
	for _, tok := range tokenize(raw) {
		term := tok.text
		neg := tok.negated
		// Unquoted "-term" and "-site:..." forms.
		if !neg && len(term) > 1 && term[0] == '-' {
			neg = true
			term = term[1:]
		}
		lower := strings.ToLower(term)

		switch {
		case tok.quoted:
			if neg {
				q.ExcludePhrases = append(q.ExcludePhrases, term)
			} else {
				q.Phrases = append(q.Phrases, term)
			}
		case strings.HasPrefix(lower, "site:"):
			v := strings.Trim(strings.TrimSpace(term[len("site:"):]), `"'`)
			v = strings.TrimRight(v, "/")
			if v != "" {
				if neg {
					q.ExcludeSites = append(q.ExcludeSites, v)
				} else {
					q.Sites = append(q.Sites, v)
				}
			}
		case strings.HasPrefix(lower, "lang:"):
			if !neg {
				for _, l := range strings.Split(term[len("lang:"):], ",") {
					l = strings.TrimSpace(l)
					if l != "" {
						q.Langs = append(q.Langs, l)
					}
				}
			}
		case strings.HasPrefix(lower, "after:"):
			if !neg {
				if d, ok := parseBound(term[len("after:"):]); ok {
					q.After = &d
					continue
				}
				textTerms = append(textTerms, term)
			}
		case strings.HasPrefix(lower, "before:"):
			if !neg {
				if d, ok := parseBound(term[len("before:"):]); ok {
					q.Before = &d
					continue
				}
				textTerms = append(textTerms, term)
			}
		default:
			if neg {
				q.ExcludeTerms = append(q.ExcludeTerms, term)
			} else {
				textTerms = append(textTerms, term)
			}
		}
	}

	q.Text = strings.Join(textTerms, " ")
	if q.Text == "" && len(q.Phrases) == 0 && len(q.Sites) == 0 {
		return q, ErrEmpty
	}
	return q, nil
}

func tokenize(s string) []token {
	var out []token
	i := 0
	for i < len(s) {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		// -"phrase"
		if s[i] == '-' && i+1 < len(s) && s[i+1] == '"' {
			i += 2
			start := i
			for i < len(s) && s[i] != '"' {
				i++
			}
			out = append(out, token{text: s[start:i], quoted: true, negated: true})
			if i < len(s) {
				i++
			}
			continue
		}
		if s[i] == '"' {
			i++
			start := i
			for i < len(s) && s[i] != '"' {
				i++
			}
			out = append(out, token{text: s[start:i], quoted: true})
			if i < len(s) {
				i++
			}
			continue
		}
		start := i
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		out = append(out, token{text: s[start:i]})
	}
	return out
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// parseBound parses 2025, 2025-06, 2025-06-01, 2025/06/01 into a UTC instant.
func parseBound(s string) (time.Time, bool) {
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006-01", "2006"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// EngineString rebuilds the query with operators, for engines that understand
// site:, quotes and exclusions natively.
func (q Query) EngineString() string {
	parts := make([]string, 0, 8+len(q.Sites)+len(q.ExcludeTerms))
	if q.Text != "" {
		parts = append(parts, q.Text)
	}
	for _, p := range q.Phrases {
		parts = append(parts, `"`+p+`"`)
	}
	for _, s := range q.Sites {
		parts = append(parts, "site:"+s)
	}
	for _, p := range q.ExcludePhrases {
		parts = append(parts, `-"`+p+`"`)
	}
	for _, s := range q.ExcludeSites {
		parts = append(parts, "-site:"+s)
	}
	for _, t := range q.ExcludeTerms {
		parts = append(parts, "-"+t)
	}
	return strings.Join(parts, " ")
}

// TextOnly is the query without any operator, for engines that cannot apply
// site:/quotes/exclusions. Use Match for post-filtering instead.
func (q Query) TextOnly() string {
	parts := make([]string, 0, 2)
	if q.Text != "" {
		parts = append(parts, q.Text)
	}
	parts = append(parts, q.Phrases...)
	return strings.Join(parts, " ")
}

// Terms returns lowercase tokens used for relevance heuristics.
func (q Query) Terms() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] || utf8.RuneCountInString(s) < 2 {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, f := range strings.Fields(q.Text) {
		add(f)
	}
	for _, p := range q.Phrases {
		add(p)
	}
	return out
}

// TimeRange maps After into a SearXNG-compatible time_range value.
func (q Query) TimeRange(now time.Time) string {
	if q.After == nil {
		return ""
	}
	age := now.Sub(*q.After)
	switch {
	case age <= 24*time.Hour:
		return "day"
	case age <= 7*24*time.Hour:
		return "week"
	case age <= 31*24*time.Hour:
		return "month"
	case age <= 366*24*time.Hour:
		return "year"
	}
	return ""
}

// AllowsDate reports whether a result published at t passes after:/before:.
// Unknown dates (zero) always pass.
func (q Query) AllowsDate(t time.Time) bool {
	if t.IsZero() {
		return true
	}
	if q.After != nil && t.Before(*q.After) {
		return false
	}
	if q.Before != nil && !t.Before(*q.Before) {
		return false
	}
	return true
}

// AllowsURL applies site: and -site: filters.
func (q Query) AllowsURL(u *url.URL) bool {
	if u == nil {
		return true
	}
	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(strings.TrimSuffix(u.Path, "/"))
	for _, s := range q.ExcludeSites {
		if siteMatches(host, path, s) {
			return false
		}
	}
	if len(q.Sites) == 0 {
		return true
	}
	for _, s := range q.Sites {
		if siteMatches(host, path, s) {
			return true
		}
	}
	return false
}

func siteMatches(host, path, site string) bool {
	site = strings.ToLower(strings.TrimSpace(site))
	if site == "" {
		return false
	}
	siteHost := site
	sitePath := ""
	if idx := strings.IndexByte(site, '/'); idx >= 0 {
		siteHost = site[:idx]
		sitePath = strings.TrimSuffix(site[idx:], "/")
	}
	siteHost = strings.TrimPrefix(siteHost, "www.")
	if host != siteHost && !strings.HasSuffix(host, "."+siteHost) {
		return false
	}
	if sitePath == "" {
		return true
	}
	return path == sitePath || strings.HasPrefix(path, sitePath+"/")
}

// ExcludesText reports whether s contains an excluded term or phrase.
func (q Query) ExcludesText(s string) bool {
	lower := strings.ToLower(s)
	for _, t := range q.ExcludeTerms {
		if strings.Contains(lower, strings.ToLower(t)) {
			return true
		}
	}
	for _, p := range q.ExcludePhrases {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// Fingerprint returns a stable cache key for the semantic query.
func (q Query) Fingerprint() string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(q.Text)))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.Phrases, "\x1f"))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.Sites, ","))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.ExcludeSites, ","))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.ExcludeTerms, ","))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.ExcludePhrases, "\x1f"))
	b.WriteByte('|')
	b.WriteString(strings.Join(q.Langs, ","))
	if q.After != nil {
		b.WriteString("|a=")
		b.WriteString(q.After.UTC().Format(time.RFC3339))
	}
	if q.Before != nil {
		b.WriteString("|b=")
		b.WriteString(q.Before.UTC().Format(time.RFC3339))
	}
	return b.String()
}

// String is a debug representation.
func (q Query) String() string {
	return fmt.Sprintf("text=%q phrases=%v sites=%v excludeSites=%v excludeTerms=%v langs=%v after=%v before=%v",
		q.Text, q.Phrases, q.Sites, q.ExcludeSites, q.ExcludeTerms, q.Langs, q.After, q.Before)
}
