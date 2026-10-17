package extract

import (
	"strings"
	"testing"
)

const articleHTML = `<!DOCTYPE html>
<html><head>
<title>测试文章 - 示例站点</title>
<meta property="og:site_name" content="示例站点">
<meta name="description" content="desc">
</head><body>
<nav>导航 菜单</nav>
<article>
<h1>测试文章标题</h1>
<p>` + "这是第一段正文，用来验证正文抽取是否正常工作。" + `</p>
<p>` + "这是第二段正文，包含更多细节内容以便通过可读性阈值。" + `</p>
<p>` + "这是第三段正文，继续补充足够多的文字内容用于测试。" + `</p>
<pre><code>func main() {}</code></pre>
</article>
<footer>版权信息</footer>
</body></html>`

func TestExtractHTML(t *testing.T) {
	page, err := Extract("https://example.com/post", []byte(articleHTML), "text/html; charset=utf-8")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !strings.Contains(page.Title, "测试文章") {
		t.Errorf("Title = %q", page.Title)
	}
	if !strings.Contains(page.Text, "第一段正文") {
		t.Errorf("Text missing body: %q", page.Text)
	}
	if strings.Contains(page.Text, "导航") {
		t.Errorf("Text should not contain nav boilerplate: %q", page.Text)
	}
	if page.Content == "" {
		t.Error("Content should not be empty")
	}
	if page.SiteName != "" && page.SiteName != "示例站点" {
		t.Errorf("SiteName = %q", page.SiteName)
	}
}

func TestExtractPlainText(t *testing.T) {
	page, err := Extract("https://example.com/raw.txt", []byte("hello\nworld"), "text/plain")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if page.Content != "hello\nworld" {
		t.Errorf("Content = %q", page.Content)
	}
}

func TestExtractEmpty(t *testing.T) {
	if _, err := Extract("https://example.com/x", nil, "text/html"); err == nil {
		t.Error("empty body should fail")
	}
	if _, err := Extract("https://example.com/x", []byte("<html><body></body></html>"), "text/html"); err == nil {
		t.Error("page without content should fail")
	}
}
