package provider

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	mrand "math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wu-jiyan/one-search-api/internal/config"
	"github.com/Wu-jiyan/one-search-api/internal/query"
)

const bingSessionTTL = 30 * time.Minute

// swappableJar lets a session rotate its cookie jar without mutating
// http.Client.Jar (which would race with in-flight requests).
type swappableJar struct {
	mu    sync.RWMutex
	inner http.CookieJar
}

func newSwappableJar() *swappableJar {
	j, _ := cookiejar.New(nil)
	return &swappableJar{inner: j}
}

func (j *swappableJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.RLock()
	inner := j.inner
	j.mu.RUnlock()
	inner.SetCookies(u, cookies)
}

func (j *swappableJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.RLock()
	inner := j.inner
	j.mu.RUnlock()
	return inner.Cookies(u)
}

func (j *swappableJar) reset() {
	j.mu.Lock()
	j.inner, _ = cookiejar.New(nil)
	j.mu.Unlock()
}

// bingFingerprint is one generated "browser": UA, client hints and language.
type bingFingerprint struct {
	UA        string
	Platform  string
	Mobile    bool
	CHUMobile string
	SecCHUA   string
	Language  string
}

// generateFingerprint builds a plausible, randomized browser fingerprint.
// PinnedUA (from config) wins when set.
func generateFingerprint(mobile bool, pinnedUA, market string, rng *mrand.Rand) bingFingerprint {
	fp := bingFingerprint{Mobile: mobile, Language: bingAcceptLanguage(market, rng)}

	// Chrome moved to a ~4-week release cadence; keep the major version in a
	// plausible recent band and derive all client hints from it.
	major := 138 + rng.Intn(11)

	if mobile {
		android := []string{"13", "14", "15"}[rng.Intn(3)]
		models := []string{"Pixel 7", "Pixel 8", "SM-S918B", "SM-A546E", "2201117TG", "V2309A", "M2012K11AC"}
		if fp.UA == "" {
			fp.UA = fmt.Sprintf("Mozilla/5.0 (Linux; Android %s; %s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Mobile Safari/537.36",
				android, models[rng.Intn(len(models))], major)
		}
		fp.Platform = "Android"
		fp.CHUMobile = "?1"
	} else {
		switch r := rng.Intn(10); {
		case r == 0:
			fp.UA = fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", major)
			fp.Platform = "macOS"
		case r == 1:
			fp.UA = fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", major)
			fp.Platform = "Linux"
		default:
			fp.UA = fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", major)
			fp.Platform = "Windows"
		}
		fp.CHUMobile = "?0"
	}
	if pinnedUA != "" {
		fp.UA = pinnedUA
	}
	fp.SecCHUA = fmt.Sprintf(`"Google Chrome";v="%d", "Chromium";v="%d", "Not_A Brand";v="24"`, major, major)
	return fp
}

// bingAcceptLanguage returns a realistic Accept-Language for the market.
func bingAcceptLanguage(market string, rng *mrand.Rand) string {
	m := strings.ToLower(strings.TrimSpace(market))
	if strings.HasPrefix(m, "zh") {
		if rng.Intn(2) == 0 {
			return "zh-CN,zh;q=0.9,en;q=0.8"
		}
		return "zh-CN,zh;q=0.9,zh-TW;q=0.8,en;q=0.7"
	}
	if strings.Contains(m, "en-gb") {
		return "en-GB,en;q=0.9,en-US;q=0.8"
	}
	if rng.Intn(2) == 0 {
		return "en-US,en;q=0.9"
	}
	return "en-US,en;q=0.9,en-GB;q=0.8"
}

// bingSession owns one Bing "browser": cookie jar, warm-up state, generated
// fingerprint and the search URL parameters a real browser/mobile app sends.
// Bing answers 200 with an empty RSS channel to bot-like requests, so this
// material changes success rates.
type bingSession struct {
	base string
	// out owns the source-address pool; srcIP is the address this session's
	// identity is bound to.
	out     *Outbound
	srcIP   string
	timeout time.Duration
	ua      string

	clientMu sync.RWMutex
	client   *http.Client

	referer     string
	extraCookie string
	market      string
	mobile      bool
	extraParams map[string]string
	warmup      bool
	pinnedUA    string
	jar         *swappableJar
	rng         *mrand.Rand

	warmMu sync.Mutex

	mu        sync.Mutex
	fp        bingFingerprint
	cvid      string
	lastQuery string
	warmedAt  time.Time
}

func newBingSession(cfg config.ProviderConfig, out *Outbound, userAgent string) *bingSession {
	jar := newSwappableJar()

	timeout := cfg.Timeout.D()
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	base := strings.TrimRight(cfg.BaseURL, "/")
	referer := strings.TrimSpace(cfg.Referer)
	if referer == "" {
		referer = base + "/"
	}
	seed := int64(binary.LittleEndian.Uint64(randomBytes(8)))
	s := &bingSession{
		base:        base,
		out:         out,
		timeout:     timeout,
		ua:          userAgent,
		referer:     referer,
		extraCookie: strings.TrimSpace(cfg.Cookies),
		market:      strings.TrimSpace(cfg.Market),
		mobile:      cfg.Mobile,
		extraParams: cfg.ExtraParams,
		warmup:      cfg.IsWarmupEnabled(),
		pinnedUA:    strings.TrimSpace(cfg.UserAgent),
		jar:         jar,
		rng:         mrand.New(mrand.NewSource(seed)),
	}
	s.rotateLocked()
	debugf("bingsession created: %s", s.describe())
	return s
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return []byte(fmt.Sprintf("%016x", time.Now().UnixNano()))
	}
	return b
}

func randomCVID() string {
	return strings.ToUpper(hex.EncodeToString(randomBytes(16)))
}

// rotateLocked switches to a brand-new browser identity and to a fresh source
// address, so the new identity never shares an upstream IP with the old one.
// The cookie jar is reset with it, keeping cookies and source address
// consistent (called under warmMu).
func (s *bingSession) rotateLocked() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fp = generateFingerprint(s.mobile, s.pinnedUA, s.market, s.rng)
	s.cvid = randomCVID()
	s.lastQuery = ""
	s.warmedAt = time.Time{}
	s.jar.reset()

	var client *http.Client
	if s.out != nil {
		s.srcIP = s.out.Next()
		client = s.out.NewClient(s.srcIP, s.timeout, s.ua)
	} else {
		client = NewHTTPClient(s.timeout, s.ua)
	}
	client.Jar = s.jar
	s.clientMu.Lock()
	s.client = client
	s.clientMu.Unlock()
}

// httpClient returns the session's current client. It is replaced on every
// identity rotation, so it is read under a lock.
func (s *bingSession) httpClient() *http.Client {
	s.clientMu.RLock()
	defer s.clientMu.RUnlock()
	return s.client
}

func (s *bingSession) currentFP() bingFingerprint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fp
}

func (s *bingSession) isWarm() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.warmedAt.IsZero() && time.Since(s.warmedAt) < bingSessionTTL
}

// invalidate forces the next request to rotate identity and warm up again.
func (s *bingSession) invalidate() {
	s.warmMu.Lock()
	s.rotateLocked()
	s.warmMu.Unlock()
}

// ensure performs the homepage visit that lets Bing set MUID/_EDGE_S/SRCHD and
// friends, exactly like a real browser landing on bing.com first.
func (s *bingSession) ensure(ctx context.Context) {
	if !s.warmup || s.isWarm() {
		return
	}
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	if s.isWarm() {
		return
	}

	target := s.base + "/"
	if s.market != "" {
		target += "?setmkt=" + url.QueryEscape(s.market)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return
	}
	s.applyHeaders(req, "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8", "none")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		debugf("bingsession warmup error: %v", err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		debugf("bingsession warmup status=%d", resp.StatusCode)
		return
	}
	s.mu.Lock()
	s.warmedAt = time.Now()
	s.mu.Unlock()
	debugf("bingsession warmed up: target=%s ua=%q", target, s.currentFP().UA)
}

// Warm performs the homepage warm-up. It is safe to call concurrently with
// searches (ensure serializes the actual request).
func (s *bingSession) Warm(ctx context.Context) { s.ensure(ctx) }

// waitRetry sleeps a short jittered backoff between same-channel retries.
func (s *bingSession) waitRetry(ctx context.Context, attempt int) {
	base := time.Duration(160+attempt*150) * time.Millisecond
	jitter := time.Duration(time.Now().UnixNano()%140) * time.Millisecond
	timer := time.NewTimer(base + jitter)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (s *bingSession) applyHeaders(req *http.Request, accept, secFetchSite string) {
	fp := s.currentFP()
	req.Header.Set("User-Agent", fp.UA)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", fp.Language)
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", secFetchSite)
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Referer", s.referer)
	req.Header.Set("sec-ch-ua", fp.SecCHUA)
	req.Header.Set("sec-ch-ua-mobile", fp.CHUMobile)
	req.Header.Set("sec-ch-ua-platform", `"`+fp.Platform+`"`)
	if s.extraCookie != "" {
		req.Header.Set("Cookie", s.extraCookie)
	}
}

func (s *bingSession) get(ctx context.Context, target, accept string) (*http.Response, error) {
	s.ensure(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	s.applyHeaders(req, accept, "same-origin")
	return s.httpClient().Do(req)
}

// noteQuery remembers the previous query for the pq= parameter.
func (s *bingSession) noteQuery(q string) {
	s.mu.Lock()
	s.lastQuery = q
	s.mu.Unlock()
}

// searchURL builds the query string a real browser or mobile app sends.
func (s *bingSession) searchURL(q query.Query, region string, rss bool) string {
	return s.searchURLPage(q, region, rss, 0)
}

// searchURLPage is searchURL with explicit pagination. first is the 1-based
// index of the first result (11 for page 2) and mirrors what Bing's own pager
// links carry: &first=11&FORM=PERE. Note that Bing only honours this for
// browser-like sessions; see TestLiveBingPagination.
func (s *bingSession) searchURLPage(q query.Query, region string, rss bool, first int) string {
	qstr := q.EngineString()

	s.mu.Lock()
	cvid := s.cvid
	prev := s.lastQuery
	s.mu.Unlock()
	if prev == "" {
		prev = qstr
	}

	v := url.Values{}
	v.Set("q", qstr)
	if s.mobile {
		v.Set("form", "LBT003")
		v.Set("PC", "EMMX01")
		v.Set("scope", "web")
		v.Set("darkschemeovr", "1")
	} else {
		v.Set("form", "QBLH")
		v.Set("sp", "-1")
		v.Set("lq", "0")
		v.Set("pq", prev)
		// sc is a client-side caret/selection hint; the server does not validate
		// it, so generate a plausible "<6..14>-<term count>" pair.
		terms := len(strings.Fields(qstr))
		if terms < 1 {
			terms = 1
		}
		v.Set("sc", fmt.Sprintf("%d-%d", 6+randIntn(9), terms))
		v.Set("qs", "n")
		v.Set("sk", "")
		v.Set("cvid", cvid)
		v.Set("ghsh", "0")
		v.Set("ghacc", "0")
		v.Set("ghpl", "")
	}
	if s.market != "" {
		v.Set("setmkt", s.market)
	}
	if q.Count > 0 && !s.mobile {
		v.Set("count", strconv.Itoa(min(q.Count, 50)))
	}
	if len(q.Langs) > 0 {
		v.Set("setlang", q.Langs[0])
	}
	if region != "" {
		v.Set("cc", region)
	}
	for k, val := range s.extraParams {
		v.Set(k, val)
	}
	if first > 1 {
		// Same shape as the pager links Bing emits in its own HTML.
		v.Set("first", strconv.Itoa(first))
		v.Set("FORM", "PERE")
	}
	if rss {
		v.Set("format", "rss")
	}
	return s.base + "/search?" + v.Encode()
}

func (s *bingSession) describe() string {
	kind := "desktop"
	if s.mobile {
		kind = "mobile"
	}
	fp := s.currentFP()
	return fmt.Sprintf("%s market=%q platform=%s base=%s src=%s", kind, s.market, fp.Platform, s.base, s.sourceIP())
}

// sourceIP returns the address this session's identity is bound to.
func (s *bingSession) sourceIP() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srcIP
}

// randIntn returns a crypto-random int in [0,n). It is safe for concurrent use,
// unlike math/rand.Rand which is only touched during session rotation.
func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(randomBytes(4)) % uint32(n))
}
