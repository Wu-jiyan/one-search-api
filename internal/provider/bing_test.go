package provider

import (
	"context"
	"encoding/base64"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wujiyan/one-search-api/internal/config"
)

const (
	rssOneItem = `<?xml version="1.0" encoding="utf-8" ?><rss version="2.0"><channel><title>bing</title><item><title>Alpha Result</title><link>https://example.com/a</link><description>alpha snippet</description><pubDate>Sat, 17 Oct 2026 12:42:00 GMT</pubDate></item></channel></rss>`
	rssEmpty   = `<?xml version="1.0" encoding="utf-8" ?><rss version="2.0"><channel><title>bing</title></channel></rss>`
)

const bingHTMLFixture = `<!DOCTYPE html><html><body><ol id="b_results">
<li class="b_algo"><h2><a href="https://example.com/a">Alpha 标题</a></h2><div class="b_caption"><p>这是 Alpha 的摘要内容，足够长。</p></div><span class="news_dt">2026年10月01日</span></li>
<li class="b_algo"><h2><a href="https://example.com/b">Beta 标题</a></h2><div class="b_caption"><p>Beta 摘要。</p></div></li>
</ol></body></html>`

const bingChallengeFixture = `<!DOCTYPE html><html><body><div>Please verify you are a human</div></body></html>`

func TestFingerprintGeneration(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	seenUA := map[string]bool{}
	seenPlatform := map[string]bool{}
	for i := 0; i < 60; i++ {
		fp := generateFingerprint(i%2 == 0, "", "zh-CN", rng)
		if fp.UA == "" || !strings.Contains(fp.UA, "Chrome/") {
			t.Fatalf("bad UA: %q", fp.UA)
		}
		if fp.SecCHUA == "" || fp.CHUMobile == "" || fp.Platform == "" {
			t.Fatalf("incomplete fingerprint: %+v", fp)
		}
		seenUA[fp.UA] = true
		seenPlatform[fp.Platform] = true
	}
	if len(seenUA) < 3 {
		t.Errorf("UA diversity too low: %d distinct", len(seenUA))
	}
	if len(seenPlatform) < 2 {
		t.Errorf("platform diversity too low: %v", seenPlatform)
	}

	pinned := generateFingerprint(false, "MyPinnedUA/1.0", "en-WW", rng)
	if pinned.UA != "MyPinnedUA/1.0" {
		t.Errorf("pinned UA lost: %q", pinned.UA)
	}
}

func TestBingSessionWarmupAndParams(t *testing.T) {
	var homeHits, searchHits int32
	var gotCookie, gotUA, gotLang, gotReferer, gotSite, gotCHMobile string
	var gotQuery url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			atomic.AddInt32(&homeHits, 1)
			http.SetCookie(w, &http.Cookie{Name: "MUID", Value: "abc123", Path: "/"})
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<html><body>home</body></html>"))
			return
		}
		atomic.AddInt32(&searchHits, 1)
		gotCookie = r.Header.Get("Cookie")
		gotUA = r.Header.Get("User-Agent")
		gotLang = r.Header.Get("Accept-Language")
		gotReferer = r.Header.Get("Referer")
		gotSite = r.Header.Get("Sec-Fetch-Site")
		gotCHMobile = r.Header.Get("sec-ch-ua-mobile")
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(rssOneItem))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{
		Name:    "bing",
		Type:    "bingrss",
		BaseURL: srv.URL,
		Market:  "zh-CN",
	})
	res, err := p.Search(context.Background(), mustQ(t, "你好"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("len = %d, want 1", len(res))
	}
	if got := atomic.LoadInt32(&homeHits); got != 1 {
		t.Errorf("homepage warm-up hits = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&searchHits); got != 1 {
		t.Errorf("search hits = %d, want 1", got)
	}
	if !strings.Contains(gotCookie, "MUID=abc123") {
		t.Errorf("warm-up cookie missing from search request: %q", gotCookie)
	}
	if gotUA == "" || strings.Contains(gotUA, "Go-http-client") || !strings.Contains(gotUA, "Chrome/") {
		t.Errorf("UA not a browser fingerprint: %q", gotUA)
	}
	if !strings.HasPrefix(gotLang, "zh-CN") {
		t.Errorf("Accept-Language = %q", gotLang)
	}
	if gotReferer != srv.URL+"/" {
		t.Errorf("Referer = %q", gotReferer)
	}
	if gotSite != "same-origin" {
		t.Errorf("Sec-Fetch-Site = %q", gotSite)
	}
	if gotCHMobile != "?0" {
		t.Errorf("sec-ch-ua-mobile = %q", gotCHMobile)
	}
	if gotQuery.Get("form") != "QBLH" || gotQuery.Get("qs") != "n" {
		t.Errorf("desktop params wrong: %v", gotQuery)
	}
	if gotQuery.Get("pq") != "你好" {
		t.Errorf("pq = %q, want the current query on first search", gotQuery.Get("pq"))
	}
	if gotQuery.Get("setmkt") != "zh-CN" {
		t.Errorf("setmkt = %q", gotQuery.Get("setmkt"))
	}
	cvid := gotQuery.Get("cvid")
	if len(cvid) != 32 || cvid != strings.ToUpper(cvid) {
		t.Errorf("cvid = %q, want 32 uppercase hex chars", cvid)
	}
	if !regexp.MustCompile(`^\d{1,2}-\d{1,2}$`).MatchString(gotQuery.Get("sc")) {
		t.Errorf("sc = %q", gotQuery.Get("sc"))
	}
}

func TestBingMobileFingerprint(t *testing.T) {
	var gotUA, gotCHMobile string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: "MUID", Value: "m", Path: "/"})
			_, _ = w.Write([]byte("<html>home</html>"))
			return
		}
		gotUA = r.Header.Get("User-Agent")
		gotCHMobile = r.Header.Get("sec-ch-ua-mobile")
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(rssOneItem))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{
		Name:    "bing-mobile",
		Type:    "bingrss",
		BaseURL: srv.URL,
		Market:  "en-WW",
		Mobile:  true,
	})
	if _, err := p.Search(context.Background(), mustQ(t, "hello")); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(gotUA, "Mobile Safari") {
		t.Errorf("mobile UA expected, got %q", gotUA)
	}
	if gotCHMobile != "?1" {
		t.Errorf("sec-ch-ua-mobile = %q", gotCHMobile)
	}
	for k, want := range map[string]string{
		"form": "LBT003", "PC": "EMMX01", "scope": "web", "darkschemeovr": "1", "setmkt": "en-WW",
	} {
		if got := gotQuery.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestBingRSSRetriesEmptyChannel(t *testing.T) {
	var rssHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: "MUID", Value: "x", Path: "/"})
			_, _ = w.Write([]byte("<html>home</html>"))
			return
		}
		n := atomic.AddInt32(&rssHits, 1)
		w.Header().Set("Content-Type", "text/xml")
		if n == 1 {
			_, _ = w.Write([]byte(rssEmpty))
			return
		}
		_, _ = w.Write([]byte(rssOneItem))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "bing", Type: "bingrss", BaseURL: srv.URL, Market: "zh-CN"})
	res, err := p.Search(context.Background(), mustQ(t, "retry-test"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("len = %d, want 1", len(res))
	}
	if got := atomic.LoadInt32(&rssHits); got != 2 {
		t.Errorf("rss attempts = %d, want 2 (empty channel must be retried)", got)
	}
}

func TestBingRSSFallsBackToHTML(t *testing.T) {
	var rssHits, htmlHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: "MUID", Value: "x", Path: "/"})
			_, _ = w.Write([]byte("<html>home</html>"))
			return
		}
		if r.URL.Query().Get("format") == "rss" {
			atomic.AddInt32(&rssHits, 1)
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(rssEmpty))
			return
		}
		atomic.AddInt32(&htmlHits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(bingHTMLFixture))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "bing", Type: "bingrss", BaseURL: srv.URL, Market: "zh-CN"})
	res, err := p.Search(context.Background(), mustQ(t, "fallback-test"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("len = %d, want 2 from the HTML fallback", len(res))
	}
	if res[0].Title != "Alpha 标题" {
		t.Errorf("first title = %q", res[0].Title)
	}
	if got := atomic.LoadInt32(&rssHits); got != 2 {
		t.Errorf("rss attempts = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&htmlHits); got != 1 {
		t.Errorf("html fallback attempts = %d, want 1", got)
	}
}

func TestBingRSSRetriesOffTopicResults(t *testing.T) {
	makeRSS := func(items [][2]string) string {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="utf-8" ?><rss version="2.0"><channel><title>bing</title>`)
		for _, it := range items {
			b.WriteString("<item><title>" + it[0] + "</title><link>" + it[1] + "</link><description>" + it[0] + "</description></item>")
		}
		b.WriteString(`</channel></rss>`)
		return b.String()
	}
	offTopic := makeRSS([][2]string{
		{"Vietnamese food guide", "https://food.example/a"},
		{"Best pho in Hanoi", "https://food.example/b"},
	})
	relevant := makeRSS([][2]string{
		{"golang context cancellation", "https://go.dev/a"},
		{"Go context cancellation patterns", "https://go.dev/b"},
	})

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: "MUID", Value: "x", Path: "/"})
			_, _ = w.Write([]byte("<html>home</html>"))
			return
		}
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/xml")
		if n == 1 {
			_, _ = w.Write([]byte(offTopic))
			return
		}
		_, _ = w.Write([]byte(relevant))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "bing", Type: "bingrss", BaseURL: srv.URL, Market: "zh-CN"})
	res, err := p.Search(context.Background(), mustQ(t, "golang context cancellation"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("attempts = %d, want 2 (off-topic results must be retried)", got)
	}
	if len(res) != 2 {
		t.Fatalf("len = %d, want 2", len(res))
	}
	for _, r := range res {
		if strings.Contains(r.URL, "food.example") {
			t.Errorf("off-topic result leaked: %s", r.URL)
		}
	}
}

func TestParseBingHTML(t *testing.T) {
	q := mustQ(t, "alpha")
	results, err := parseBingHTML([]byte(bingHTMLFixture), "bing", q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	if results[0].Title != "Alpha 标题" || results[0].URL != "https://example.com/a" {
		t.Errorf("first result = %+v", results[0])
	}
	if results[0].Snippet == "" {
		t.Error("snippet missing")
	}
	if results[0].Date.IsZero() || results[0].Date.Year() != 2026 {
		t.Errorf("date = %v", results[0].Date)
	}
	if results[1].Title != "Beta 标题" {
		t.Errorf("second title = %q", results[1].Title)
	}
}

func TestParseBingHTMLNoResults(t *testing.T) {
	body := `<html><body><div id="b_no">没有与此相关的结果</div></body></html>`
	results, err := parseBingHTML([]byte(body), "bing", mustQ(t, "nothing"))
	if err != nil {
		t.Fatalf("legit no-results must not error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}

func TestParseBingHTMLChallenge(t *testing.T) {
	if _, err := parseBingHTML([]byte(bingChallengeFixture), "bing", mustQ(t, "x")); err == nil {
		t.Error("challenge page must be reported as an error")
	}
}

func TestBingHTMLRetriesOnChallenge(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.SetCookie(w, &http.Cookie{Name: "MUID", Value: "x", Path: "/"})
			_, _ = w.Write([]byte("<html>home</html>"))
			return
		}
		n := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if n == 1 {
			_, _ = w.Write([]byte(bingChallengeFixture))
			return
		}
		_, _ = w.Write([]byte(bingHTMLFixture))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "bing", Type: "binghtml", BaseURL: srv.URL, Market: "zh-CN"})
	res, err := p.Search(context.Background(), mustQ(t, "challenge-test"))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("len = %d, want 2", len(res))
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

func TestUnwrapBingRedirect(t *testing.T) {
	target := "https://example.com/deep/page?x=1"
	enc := base64.RawURLEncoding.EncodeToString([]byte(target))
	href := "https://www.bing.com/ck/a?!&&p=abc&u=a1" + enc + "&ntb=1"
	if got := unwrapBingRedirect(href); got != target {
		t.Errorf("unwrap = %q, want %q", got, target)
	}
	if got := unwrapBingRedirect("https://example.com/direct"); got != "https://example.com/direct" {
		t.Errorf("direct link changed: %q", got)
	}
	if got := unwrapBingRedirect("//www.bing.com/ck/a?u=a1" + enc); got != target {
		t.Errorf("scheme-relative unwrap = %q", got)
	}
}

func TestProviderUserAgentApplied(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	p := newTestProvider(t, config.ProviderConfig{Name: "sx", Type: "searxng", BaseURL: srv.URL})
	if _, err := p.Search(context.Background(), mustQ(t, "ua-test")); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotUA != "test-agent" {
		t.Errorf("User-Agent = %q, want the configured fallback", gotUA)
	}
}

func TestSessionRotationChangesIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>home</html>"))
	}))
	defer srv.Close()

	client := NewHTTPClient(time.Second, "x")
	sess := newBingSession(config.ProviderConfig{
		Name: "bing", BaseURL: srv.URL, Market: "zh-CN",
	}, client, "")

	first := sess.currentFP()
	firstCVID := sess.cvid
	sess.invalidate()
	second := sess.currentFP()
	if first.UA == second.UA && first.SecCHUA == second.SecCHUA {
		t.Error("fingerprint did not rotate")
	}
	if firstCVID == sess.cvid {
		t.Error("cvid did not rotate")
	}
}
