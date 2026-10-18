package enrich

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSnippetCleansAndClips(t *testing.T) {
	raw := `<p>Hello <b>world</b> &amp; friends</p>`
	got := Snippet(raw)
	if got != "Hello world & friends" {
		t.Errorf("Snippet = %q", got)
	}

	long := strings.Repeat("这是一个很长的句子。", 60)
	clipped := Snippet(long)
	if n := utf8.RuneCountInString(clipped); n > 360 {
		t.Errorf("snippet length = %d, want <= 360", n)
	}
	if !strings.HasSuffix(clipped, "。") && !strings.HasSuffix(clipped, "…") {
		t.Errorf("snippet should end at a boundary, got %q", clipped)
	}
}

func TestSummaryFromContent(t *testing.T) {
	content := "# 标题\n\n" + strings.Repeat("这是正文中的一句话。", 100)
	got := Summary(content, nil)
	n := utf8.RuneCountInString(got)
	if n < 400 || n > 1600 {
		t.Errorf("summary length = %d, want 400..1600", n)
	}
	if strings.Contains(got, "#") {
		t.Errorf("summary still contains markdown heading marker: %q", got[:10])
	}
}

func TestSummaryFromSnippets(t *testing.T) {
	got := Summary("", []string{"第一个摘要。", "第二个摘要。", "第一个摘要。"})
	if !strings.Contains(got, "第一个摘要") || !strings.Contains(got, "第二个摘要") {
		t.Errorf("summary = %q", got)
	}
	if strings.Count(got, "第一个摘要") != 1 {
		t.Errorf("duplicate snippet not removed: %q", got)
	}
	if Summary("", nil) != "" {
		t.Errorf("empty input should yield empty summary")
	}
}

func TestSiteNameAndIcon(t *testing.T) {
	host := "www.github.com"
	if got := SiteName(host, ""); got == nil || *got != "GitHub" {
		t.Errorf("SiteName = %v", got)
	}
	if got := SiteName(host, "Fetched Name"); got == nil || *got != "Fetched Name" {
		t.Errorf("fetched SiteName = %v", got)
	}
	if got := SiteName("unknown-domain.example", ""); got == nil || *got != "unknown-domain.example" {
		t.Errorf("fallback SiteName = %v", got)
	}
	if got := Icon("example.com", "https://{host}/favicon.ico"); got == nil || *got != "https://example.com/favicon.ico" {
		t.Errorf("Icon = %v", got)
	}
	if got := Icon("", "https://{host}/favicon.ico"); got != nil {
		t.Errorf("Icon with empty host = %v, want nil", got)
	}
}

func TestDatePtr(t *testing.T) {
	if DatePtr(time.Time{}) != nil {
		t.Error("zero time should map to nil")
	}
	d := time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	got := DatePtr(d)
	if got == nil || got.Location() != time.UTC || got.Hour() != 0 {
		t.Errorf("DatePtr = %v", got)
	}
}

func TestNormalizeTitle(t *testing.T) {
	if got := NormalizeTitle("Hello, World! 你好"); got != "helloworld你好" {
		t.Errorf("NormalizeTitle = %q", got)
	}
}

func TestMarkdownToText(t *testing.T) {
	md := "# 标题\n\n![img](https://x/y.png)\n\n**加粗** 和 [链接](https://a.b) 以及 `code`。"
	got := MarkdownToText(md)
	for _, bad := range []string{"#", "![", "](", "**", "`"} {
		if strings.Contains(got, bad) {
			t.Errorf("MarkdownToText left %q in %q", bad, got)
		}
	}
	if !strings.Contains(got, "加粗") || !strings.Contains(got, "链接") {
		t.Errorf("MarkdownToText lost text: %q", got)
	}
}
