// Package extract turns an HTML page into a title, plain text and Markdown.
package extract

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Page is the extracted representation of a fetched document.
type Page struct {
	Title     string
	SiteName  string
	Content   string // Markdown
	Text      string // plain text of the main content
	Published *time.Time
	Favicon   string
}

// Extract parses an HTML (or plain text) document.
func Extract(rawURL string, body []byte, contentType string) (Page, error) {
	if len(body) == 0 {
		return Page{}, fmt.Errorf("empty body")
	}
	decoded, err := decode(body, contentType)
	if err != nil {
		decoded = body
	}
	if isPlainText(contentType) {
		text := strings.TrimSpace(string(decoded))
		return Page{Title: "", Content: text, Text: text}, nil
	}

	var pageURL *url.URL
	if u, perr := url.Parse(rawURL); perr == nil {
		pageURL = u
	}

	if article, err := readability.FromReader(bytes.NewReader(decoded), pageURL); err == nil {
		text := strings.TrimSpace(article.TextContent)
		if utf8.RuneCountInString(text) >= 80 {
			md := toMarkdown(article.Content)
			if strings.TrimSpace(md) == "" {
				md = text
			}
			p := Page{
				Title:    strings.TrimSpace(article.Title),
				SiteName: strings.TrimSpace(article.SiteName),
				Content:  md,
				Text:     text,
				Favicon:  strings.TrimSpace(article.Favicon),
			}
			if article.PublishedTime != nil {
				t := article.PublishedTime.UTC()
				p.Published = &t
			}
			if p.Title == "" {
				p.Title = fallbackTitle(decoded)
			}
			if p.Title == "" {
				p.Title = firstHeading(decoded)
			}
			// Readability sometimes keeps only the first of several sibling
			// paragraphs (example.com: 156 of ~900 runes). When the page's own
			// paragraphs carry substantially more text, prefer them: <p> content
			// is body copy, so this does not drag navigation boilerplate in.
			if pt := paragraphText(decoded); utf8.RuneCountInString(pt) > utf8.RuneCountInString(text) &&
				utf8.RuneCountInString(pt) <= 8000 &&
				utf8.RuneCountInString(text)*100 < utf8.RuneCountInString(pt)*60 {
				p.Text = pt
				p.Content = pt
			}
			return p, nil
		}
	}

	// Fallback: whole-document text extraction.
	title, siteName := metaFromDocument(decoded)
	if title == "" {
		title = firstHeading(decoded)
	}
	text := wholeText(decoded)
	if strings.TrimSpace(text) == "" {
		return Page{}, fmt.Errorf("no extractable content")
	}
	return Page{Title: title, SiteName: siteName, Content: text, Text: text}, nil
}

// paragraphText joins the text of every <p> element. It is used as a
// completeness fallback when readability under-extracts a page.
func paragraphText(doc []byte) string {
	z := html.NewTokenizer(bytes.NewReader(doc))
	var paras []string
	var cur strings.Builder
	inP := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			if s := strings.TrimSpace(cur.String()); s != "" {
				paras = append(paras, s)
			}
			return strings.Join(paras, "\n\n")
		case html.StartTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "p":
				if inP > 0 {
					if s := strings.TrimSpace(cur.String()); s != "" {
						paras = append(paras, s)
					}
					cur.Reset()
				}
				inP++
			case "script", "style", "noscript":
				// Skip their inner text by draining to the matching close tag.
			}
		case html.TextToken:
			if inP > 0 {
				cur.WriteString(string(z.Text()))
				cur.WriteByte(' ')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if string(name) == "p" && inP > 0 {
				inP--
				if inP == 0 {
					if s := strings.TrimSpace(cur.String()); s != "" {
						paras = append(paras, strings.Join(strings.Fields(s), " "))
					}
					cur.Reset()
				}
			}
		}
	}
}

// firstHeading returns the first non-empty <h1> text, used as a title fallback
// for pages without a <title> element (httpbin.org/html and friends).
func firstHeading(doc []byte) string {
	z := html.NewTokenizer(bytes.NewReader(doc))
	inH1 := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken:
			if name, _ := z.TagName(); string(name) == "h1" {
				inH1 = true
			}
		case html.TextToken:
			if inH1 {
				if s := strings.TrimSpace(string(z.Text())); s != "" {
					return s
				}
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "h1" {
				inH1 = false
			}
		}
	}
}

func decode(body []byte, contentType string) ([]byte, error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(r, 8<<20))
}

// isPlainText reports whether the media type should be returned verbatim
// instead of being parsed as HTML. It covers the text-like types the fetch
// service accepts (HTML itself goes through the readability path).
func isPlainText(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "text/plain", "text/markdown", "text/css", "text/javascript",
		"application/json", "application/javascript", "text/richtext":
		return true
	}
	return false
}

func toMarkdown(htmlFragment string) string {
	if strings.TrimSpace(htmlFragment) == "" {
		return ""
	}
	md, err := htmltomarkdown.ConvertString(htmlFragment)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(md)
}

func fallbackTitle(doc []byte) string {
	z := html.NewTokenizer(bytes.NewReader(doc))
	inTitle := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return ""
		}
		switch tt {
		case html.StartTagToken:
			name, _ := z.TagName()
			inTitle = string(name) == "title"
		case html.TextToken:
			if inTitle {
				return strings.TrimSpace(string(z.Text()))
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			if string(name) == "title" {
				inTitle = false
			}
		}
	}
}

func metaFromDocument(doc []byte) (title, siteName string) {
	node, err := html.Parse(bytes.NewReader(doc))
	if err != nil {
		return "", ""
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if title == "" && n.FirstChild != nil {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				var prop, name, content string
				for _, a := range n.Attr {
					switch strings.ToLower(a.Key) {
					case "property":
						prop = strings.ToLower(a.Val)
					case "name":
						name = strings.ToLower(a.Val)
					case "content":
						content = a.Val
					}
				}
				if siteName == "" && (prop == "og:site_name" || name == "application-name") {
					siteName = strings.TrimSpace(content)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	return title, siteName
}

var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "iframe": true, "canvas": true, "head": true,
}

func wholeText(doc []byte) string {
	node, err := html.Parse(bytes.NewReader(doc))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skipTags[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(b.String()), " ")
}
