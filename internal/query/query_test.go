package query

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseOperators(t *testing.T) {
	raw := `DeepSeek V4 site:github.com -site:example.com "精确短语" -"release notes" -广告 lang:zh,en after:2025 before:2026-06`
	q, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if q.Text != "DeepSeek V4" {
		t.Errorf("Text = %q, want %q", q.Text, "DeepSeek V4")
	}
	if len(q.Sites) != 1 || q.Sites[0] != "github.com" {
		t.Errorf("Sites = %v", q.Sites)
	}
	if len(q.ExcludeSites) != 1 || q.ExcludeSites[0] != "example.com" {
		t.Errorf("ExcludeSites = %v", q.ExcludeSites)
	}
	if len(q.Phrases) != 1 || q.Phrases[0] != "精确短语" {
		t.Errorf("Phrases = %v", q.Phrases)
	}
	if len(q.ExcludePhrases) != 1 || q.ExcludePhrases[0] != "release notes" {
		t.Errorf("ExcludePhrases = %v", q.ExcludePhrases)
	}
	if len(q.ExcludeTerms) != 1 || q.ExcludeTerms[0] != "广告" {
		t.Errorf("ExcludeTerms = %v", q.ExcludeTerms)
	}
	if len(q.Langs) != 2 || q.Langs[0] != "zh" || q.Langs[1] != "en" {
		t.Errorf("Langs = %v", q.Langs)
	}
	if q.After == nil || !q.After.Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("After = %v", q.After)
	}
	if q.Before == nil || !q.Before.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Before = %v", q.Before)
	}

	eng := q.EngineString()
	for _, want := range []string{`"精确短语"`, "site:github.com", "-site:example.com", "-广告", `-"release notes"`} {
		if !strings.Contains(eng, want) {
			t.Errorf("EngineString() = %q, missing %q", eng, want)
		}
	}
}

func TestParseValidation(t *testing.T) {
	if _, err := Parse("   "); err != ErrEmpty {
		t.Errorf("blank query err = %v, want ErrEmpty", err)
	}
	long := strings.Repeat("啊", 129)
	if _, err := Parse(long); err != ErrTooLong {
		t.Errorf("long query err = %v, want ErrTooLong", err)
	}
	if _, err := Parse(strings.Repeat("啊", 128)); err != nil {
		t.Errorf("128-rune query should parse: %v", err)
	}
	if _, err := Parse("site:github.com"); err != nil {
		t.Errorf("operator-only query should parse: %v", err)
	}
}

func TestAllowsDate(t *testing.T) {
	q, err := Parse("test after:2025 before:2026-06")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		date time.Time
		want bool
	}{
		{"unknown", time.Time{}, true},
		{"before lower", time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), false},
		{"inside", time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), true},
		{"at upper", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		if got := q.AllowsDate(tc.date); got != tc.want {
			t.Errorf("%s: AllowsDate(%v) = %v, want %v", tc.name, tc.date, got, tc.want)
		}
	}
}

func TestAllowsURL(t *testing.T) {
	q, err := Parse("site:github.com -site:gist.github.com test")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"https://github.com/foo":      true,
		"https://docs.github.com/bar": true,
		"https://gist.github.com/x":   false,
		"https://gitlab.com/foo":      false,
		"https://notgithub.com/foo":   false,
	}
	for raw, want := range cases {
		u, canon := parseURL(t, raw)
		_ = canon
		if got := q.AllowsURL(u); got != want {
			t.Errorf("AllowsURL(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestTimeRange(t *testing.T) {
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		expr string
		want string
	}{
		{"x after:2026-10-16", "day"},
		{"x after:2026-10-13", "week"},
		{"x after:2026-09-25", "month"},
		{"x after:2026-01-01", "year"},
		{"x after:2020-01-01", ""},
		{"x", ""},
	}
	for _, tc := range cases {
		q, err := Parse(tc.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.expr, err)
		}
		if got := q.TimeRange(now); got != tc.want {
			t.Errorf("TimeRange(%q) = %q, want %q", tc.expr, got, tc.want)
		}
	}
}

func TestExcludesText(t *testing.T) {
	q, err := Parse("test -广告 -\"release notes\"")
	if err != nil {
		t.Fatal(err)
	}
	if !q.ExcludesText("这里有广告内容") {
		t.Error("expected excluded term to match")
	}
	if !q.ExcludesText("The RELEASE NOTES are here") {
		t.Error("expected excluded phrase to match case-insensitively")
	}
	if q.ExcludesText("clean content") {
		t.Error("unexpected exclusion")
	}
}

func TestFingerprintStable(t *testing.T) {
	a, _ := Parse("Hello world site:a.com")
	b, _ := Parse("  hello   world  site:a.com ")
	if a.Fingerprint() != b.Fingerprint() {
		t.Errorf("fingerprints differ:\n%q\n%q", a.Fingerprint(), b.Fingerprint())
	}
	c, _ := Parse("hello world site:b.com")
	if a.Fingerprint() == c.Fingerprint() {
		t.Error("different queries share a fingerprint")
	}
}

func parseURL(t *testing.T, raw string) (*url.URL, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return u, u.String()
}
