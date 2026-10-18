// Package fetchguard implements SSRF protection for outbound page fetches:
// URL validation, IP-range blocking, a dialer pinned to pre-validated IPs
// (defeating DNS rebinding) and per-hop redirect re-validation.
package fetchguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrInvalid means the URL is structurally unacceptable (HTTP 400).
var ErrInvalid = errors.New("invalid url")

// ErrBlocked means the URL targets a blocked destination (HTTP 403).
var ErrBlocked = errors.New("url blocked by security policy")

// Specific validation failures, each mapped to the reference API's message.
var (
	ErrEmptyURL    = fmt.Errorf("%w: empty url", ErrInvalid)
	ErrURLTooLong  = fmt.Errorf("%w: url too long", ErrInvalid)
	ErrBadScheme   = fmt.Errorf("%w: bad scheme", ErrInvalid)
	ErrNoHost      = fmt.Errorf("%w: missing host", ErrInvalid)
	ErrCredentials = fmt.Errorf("%w: credentials in url", ErrInvalid)
	ErrBadPort     = fmt.Errorf("%w: bad port", ErrInvalid)
)

var blockedCIDRs = mustParseCIDRs(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"::/128",
	"::1/128",
	"100::/64",
	"2001:db8::/32",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("fetchguard: bad CIDR " + c)
		}
		out = append(out, n)
	}
	return out
}

var blockedHosts = map[string]bool{
	"localhost":                true,
	"localhost.localdomain":    true,
	"metadata.google.internal": true,
	"metadata.goog":            true,
	"instance-data":            true,
	"metadata":                 true,
}

var blockedHostSuffixes = []string{".local", ".internal", ".localhost", ".home.arpa", ".lan", ".intranet", ".corp"}

// Guard validates outbound fetch targets.
type Guard struct {
	allowPrivate bool
	ports        map[int]bool
	maxRedirects int
	dialer       *net.Dialer
}

// New builds a guard. allowPrivate disables RFC1918/loopback blocking (local
// debugging only). Ports is the allowlist of destination ports.
func New(allowPrivate bool, ports []int, maxRedirects int) *Guard {
	if maxRedirects <= 0 {
		maxRedirects = 5
	}
	pm := make(map[int]bool, len(ports))
	for _, p := range ports {
		pm[p] = true
	}
	if len(pm) == 0 {
		pm[80] = true
		pm[443] = true
	}
	return &Guard{
		allowPrivate: allowPrivate,
		ports:        pm,
		maxRedirects: maxRedirects,
		dialer:       &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second},
	}
}

// ValidateURL checks scheme, host, credentials, port and hostname denylist.
// It does not resolve DNS.
func (g *Guard) ValidateURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrEmptyURL
	}
	if len(raw) > 8192 {
		return nil, ErrURLTooLong
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadScheme, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme must be http or https", ErrBadScheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, ErrNoHost
	}
	if u.User != nil {
		return nil, ErrCredentials
	}
	if u.Port() != "" {
		port, perr := net.LookupPort("tcp", u.Port())
		if perr != nil {
			return nil, fmt.Errorf("%w: invalid port", ErrBadPort)
		}
		if !g.ports[port] {
			return nil, fmt.Errorf("%w: port %d is not allowed", ErrBlocked, port)
		}
	} else if !g.ports[80] && !g.ports[443] {
		return nil, fmt.Errorf("%w: no default port allowed", ErrBlocked)
	}

	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if blockedHosts[host] {
		return nil, fmt.Errorf("%w: host %q is not allowed", ErrBlocked, host)
	}
	for _, suffix := range blockedHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return nil, fmt.Errorf("%w: host %q is not allowed", ErrBlocked, host)
		}
	}
	if ip := net.ParseIP(host); ip != nil && g.blockedIP(ip) {
		return nil, fmt.Errorf("%w: ip %s is not allowed", ErrBlocked, host)
	}
	return u, nil
}

func (g *Guard) blockedIP(ip net.IP) bool {
	if g.allowPrivate {
		return false
	}
	// Normalize IPv4-mapped IPv6 (::ffff:127.0.0.1) to its IPv4 form.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else if v6 := ip.To16(); v6 != nil {
		ip = v6
	} else {
		return true
	}
	for _, n := range blockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	return false
}

// DialContext resolves the host, validates every resolved IP, then dials a
// validated IP directly so a second DNS answer cannot redirect us.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid port", ErrInvalid)
	}
	if !g.ports[port] {
		return nil, fmt.Errorf("%w: port %d is not allowed", ErrBlocked, port)
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %q: no addresses", host)
	}
	for _, ipa := range ips {
		if g.blockedIP(ipa.IP) {
			return nil, fmt.Errorf("%w: host %q resolves to blocked ip %s", ErrBlocked, host, ipa.IP)
		}
	}
	var lastErr error
	for _, ipa := range ips {
		conn, derr := g.dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), portStr))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	return nil, fmt.Errorf("dial %q: %w", host, lastErr)
}

// NewClient returns an HTTP client whose transport is SSRF-safe.
func (g *Guard) NewClient(userAgent string, timeout time.Duration, maxRedirects int) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		// No proxy: a proxy would bypass our dialer checks.
		Proxy:                 nil,
		DialContext:           g.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: too many redirects", ErrBlocked)
			}
			if _, err := g.ValidateURL(req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

// ClassifyError maps fetchguard errors to an HTTP status code.
func ClassifyError(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrBlocked):
		return http.StatusForbidden
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}
