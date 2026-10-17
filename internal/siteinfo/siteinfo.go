// Package siteinfo maps domains to authority scores, display names and icons.
package siteinfo

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// exact maps a full host to its authority score.
var exact = map[string]int{
	// Official docs, source code, standards bodies.
	"github.com": 3, "gitlab.com": 3, "go.dev": 3, "golang.org": 3,
	"python.org": 3, "nodejs.org": 3, "rust-lang.org": 3, "developer.mozilla.org": 3,
	"kernel.org": 3, "w3.org": 3, "ietf.org": 3, "rfc-editor.org": 3,
	"docs.deepseek.com": 4, "api-docs.deepseek.com": 4, "platform.deepseek.com": 4,
	"openai.com": 3, "anthropic.com": 3, "ai.google": 3, "deepmind.google": 3,
	"cloudflare.com": 3, "aws.amazon.com": 3, "azure.microsoft.com": 3,
	"kubernetes.io": 3, "docker.com": 3, "postgresql.org": 3, "mysql.com": 3,
	"nginx.org": 3, "apache.org": 3, "redis.io": 3, "sqlite.org": 3,
	// Reference / encyclopedic.
	"wikipedia.org": 3, "britannica.com": 3,
	// Wire services and major outlets.
	"reuters.com": 3, "apnews.com": 3, "bbc.com": 3, "bbc.co.uk": 3,
	"nytimes.com": 3, "wsj.com": 3, "bloomberg.com": 3, "ft.com": 3,
	"theguardian.com": 3, "economist.com": 3, "cnn.com": 3, "npr.org": 3,
	"xinhuanet.com": 3, "news.cn": 3, "people.com.cn": 3, "cctv.com": 3,
	"thepaper.cn": 3, "caixin.com": 3, "yicai.com": 3, "36kr.com": 2,
	// Journals / academic.
	"nature.com": 3, "science.org": 3, "sciencedirect.com": 3, "springer.com": 3,
	"arxiv.org": 3, "openalex.org": 3, "pubmed.ncbi.nlm.nih.gov": 3,
	"acm.org": 3, "ieee.org": 3, "jstor.org": 3, "semanticscholar.org": 3,
	// Tech media / vendor engineering.
	"infoq.com": 2, "sspai.com": 2, "arstechnica.com": 2, "theregister.com": 2,
	"techcrunch.com": 2, "wired.com": 2, "verge.com": 2, "zdnet.com": 2,
	"stackoverflow.com": 2, "serverfault.com": 2, "superuser.com": 2,
	"hackernews.ycombinator.com": 1, "news.ycombinator.com": 1,
	// UGC / forums / blogs.
	"reddit.com": 1, "zhihu.com": 1, "v2ex.com": 1, "csdn.net": 1,
	"juejin.cn": 1, "cnblogs.com": 1, "segmentfault.com": 1, "jianshu.com": 1,
	"weibo.com": 1, "douban.com": 1, "tieba.baidu.com": 1, "bilibili.com": 1,
	"youtube.com": 1, "medium.com": 1, "dev.to": 1, "substack.com": 1,
	"twitter.com": 1, "x.com": 1, "facebook.com": 1, "linkedin.com": 1,
	// Low-signal aggregators.
	"baidu.com": 0, "hao123.com": 0, "so.com": 0, "sogou.com": 0,
	"zhidao.baidu.com": 0, "wenku.baidu.com": 0,
}

// suffixes apply when host equals or is a subdomain of the entry.
var suffixes = []struct {
	suffix string
	score  int
}{
	{"gov.cn", 4}, {"gov", 4}, {"mil", 4}, {"int", 4}, {"europa.eu", 4}, {"un.org", 4}, {"who.int", 4},
	{"edu.cn", 3}, {"edu", 3}, {"ac.cn", 3}, {"ac.uk", 3}, {"ac.jp", 3},
}

// display names for well-known hosts (registered domain -> label).
var names = map[string]string{
	"github.com": "GitHub", "gitlab.com": "GitLab", "go.dev": "Go", "golang.org": "Go",
	"python.org": "Python", "nodejs.org": "Node.js", "rust-lang.org": "Rust",
	"developer.mozilla.org": "MDN", "stackoverflow.com": "Stack Overflow",
	"wikipedia.org": "Wikipedia", "zhihu.com": "知乎", "weibo.com": "微博",
	"bilibili.com": "哔哩哔哩", "douban.com": "豆瓣", "v2ex.com": "V2EX",
	"csdn.net": "CSDN", "juejin.cn": "掘金", "cnblogs.com": "博客园",
	"segmentfault.com": "SegmentFault", "jianshu.com": "简书",
	"reddit.com": "Reddit", "medium.com": "Medium", "youtube.com": "YouTube",
	"twitter.com": "X", "x.com": "X", "facebook.com": "Facebook", "linkedin.com": "LinkedIn",
	"nytimes.com": "The New York Times", "bbc.com": "BBC", "bbc.co.uk": "BBC",
	"reuters.com": "Reuters", "bloomberg.com": "Bloomberg", "wsj.com": "WSJ",
	"ft.com": "Financial Times", "theguardian.com": "The Guardian",
	"36kr.com": "36氪", "sspai.com": "少数派", "infoq.com": "InfoQ",
	"thepaper.cn": "澎湃新闻", "xinhuanet.com": "新华网", "people.com.cn": "人民网",
	"nature.com": "Nature", "science.org": "Science", "arxiv.org": "arXiv",
	"openai.com": "OpenAI", "anthropic.com": "Anthropic", "deepseek.com": "DeepSeek",
	"cloudflare.com": "Cloudflare", "aws.amazon.com": "AWS", "azure.microsoft.com": "Azure",
	"docker.com": "Docker", "kubernetes.io": "Kubernetes", "kernel.org": "Linux",
}

// Authority returns 0..4, or -1 when the domain is unknown.
func Authority(host string) int {
	host = normalizeHost(host)
	if host == "" {
		return -1
	}
	if v, ok := exact[host]; ok {
		return v
	}
	for _, s := range suffixes {
		if host == s.suffix || strings.HasSuffix(host, "."+s.suffix) {
			return s.score
		}
	}
	// Try the registered domain for exact matches (e.g. blog.github.com).
	if rd := RegistrableDomain(host); rd != "" && rd != host {
		if v, ok := exact[rd]; ok {
			return v
		}
		for _, s := range suffixes {
			if rd == s.suffix || strings.HasSuffix(rd, "."+s.suffix) {
				return s.score
			}
		}
	}
	return -1
}

// Name returns a human site name for host, or "" when unknown.
func Name(host string) string {
	host = normalizeHost(host)
	if host == "" {
		return ""
	}
	if v, ok := names[host]; ok {
		return v
	}
	rd := RegistrableDomain(host)
	if v, ok := names[rd]; ok {
		return v
	}
	return ""
}

// RegistrableDomain returns the eTLD+1 for host, or host itself on failure.
func RegistrableDomain(host string) string {
	host = normalizeHost(host)
	if host == "" {
		return ""
	}
	if rd, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return rd
	}
	return host
}

// Icon renders the favicon URL template. An empty template yields "".
func Icon(host, template string) string {
	host = normalizeHost(host)
	if host == "" || template == "" {
		return ""
	}
	return strings.ReplaceAll(template, "{host}", host)
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimSuffix(host, ".")
	return host
}
