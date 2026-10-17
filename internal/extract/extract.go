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
			return p, nil
		}
	}

	// Fallback: whole-document text extraction.
	title, siteName := metaFromDocument(decoded)
	text := wholeText(decoded)
	if strings.TrimSpace(text) == "" {
		return Page{}, fmt.Errorf("no extractable content")
	}
	return Page{Title: title, SiteName: siteName, Content: text, Text: text}, nil
}

func decode(body []byte, contentType string) ([]byte, error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(r, 8<<20))
}

func isPlainText(contentType string) bool {
	ct := strings.ToLower(contentType)
	if ct == "" {
		return false
	}
	return strings.HasPrefix(ct, "text/plain") || strings.Contains(ct, "application/json")
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
