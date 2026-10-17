package siteinfo

import "testing"

func TestAuthority(t *testing.T) {
	cases := []struct {
		host string
		want int
	}{
		{"github.com", 3},
		{"docs.github.com", 3},
		{"api-docs.deepseek.com", 4},
		{"foo.gov.cn", 4},
		{"agency.gov", 4},
		{"mit.edu", 3},
		{"reddit.com", 1},
		{"www.zhihu.com", 1},
		{"baidu.com", 0},
		{"totally-unknown.example", -1},
		{"", -1},
	}
	for _, tc := range cases {
		if got := Authority(tc.host); got != tc.want {
			t.Errorf("Authority(%q) = %d, want %d", tc.host, got, tc.want)
		}
	}
}

func TestName(t *testing.T) {
	cases := map[string]string{
		"www.github.com": "GitHub",
		"zhihu.com":      "知乎",
		"unknown.test":   "",
	}
	for host, want := range cases {
		if got := Name(host); got != want {
			t.Errorf("Name(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestRegistrableDomain(t *testing.T) {
	if got := RegistrableDomain("a.b.example.co.uk"); got != "example.co.uk" {
		t.Errorf("RegistrableDomain = %q", got)
	}
	if got := RegistrableDomain("www.github.com"); got != "github.com" {
		t.Errorf("RegistrableDomain = %q", got)
	}
}

func TestIcon(t *testing.T) {
	if got := Icon("www.example.com", "https://{host}/favicon.ico"); got != "https://example.com/favicon.ico" {
		t.Errorf("Icon = %q", got)
	}
	if got := Icon("", "https://{host}/favicon.ico"); got != "" {
		t.Errorf("Icon(empty) = %q", got)
	}
	if got := Icon("example.com", ""); got != "" {
		t.Errorf("Icon(no template) = %q", got)
	}
}
