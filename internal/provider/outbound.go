package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Outbound describes how a provider reaches the internet: an optional HTTP
// proxy and a pool of source addresses to bind outgoing connections to.
//
// Binding a source address matters on hosts with a routed IPv6 prefix: every
// distinct source address is a distinct client as far as upstream per-IP rate
// limiting and bot detection are concerned.
type Outbound struct {
	proxy string
	ips   []string

	mu      sync.Mutex
	rr      uint64
	clients map[string]*http.Client
}

// NewOutbound builds an Outbound from configuration. The pool may contain the
// literal "auto", which expands to the host's public addresses.
func NewOutbound(proxy string, pool []string) (*Outbound, error) {
	ips, err := ExpandSourcePool(pool)
	if err != nil {
		return nil, err
	}
	o := &Outbound{proxy: strings.TrimSpace(proxy), ips: ips, clients: map[string]*http.Client{}}
	if o.proxy != "" {
		u, err := url.Parse(o.proxy)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid proxy %q", o.proxy)
		}
	}
	return o, nil
}

// ExpandSourcePool resolves a configured source-address pool into concrete
// addresses. The literal "auto" expands to the host's public unicast
// addresses, IPv6 first: on a host with a routed IPv6 prefix those are the
// addresses that actually multiply the number of distinct upstream clients.
func ExpandSourcePool(pool []string) ([]string, error) {
	out := make([]string, 0, len(pool))
	for _, raw := range pool {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.EqualFold(raw, "auto") {
			out = append(out, hostPublicIPs()...)
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			return nil, fmt.Errorf("invalid source ip %q", raw)
		}
		out = append(out, ip.String())
	}
	return dedupeStrings(out), nil
}

// Offset skips n addresses before the round-robin starts, so two providers
// sharing one pool do not begin on the same source address.
func (o *Outbound) Offset(n uint64) {
	if o == nil || len(o.ips) == 0 {
		return
	}
	o.mu.Lock()
	o.rr += n
	o.mu.Unlock()
}

// PoolSize reports how many source addresses are available.
func (o *Outbound) PoolSize() int {
	if o == nil {
		return 0
	}
	return len(o.ips)
}

// Next returns the next source address in round-robin order, or "" when the
// pool is empty.
func (o *Outbound) Next() string {
	if o == nil || len(o.ips) == 0 {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	ip := o.ips[int(o.rr%uint64(len(o.ips)))]
	o.rr++
	return ip
}

// NewClient builds a fresh HTTP client bound to the given source address.
func (o *Outbound) NewClient(localIP string, timeout time.Duration, userAgent string) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 o.proxyFunc(),
		DialContext:           o.dialContext(localIP),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &uaTransport{
			base: transport,
			ua:   userAgent,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// CachedClient returns a shared client per source address, so rotating
// addresses does not throw away the connection pool on every request.
func (o *Outbound) CachedClient(localIP string, timeout time.Duration, userAgent string) *http.Client {
	if o == nil {
		return NewHTTPClient(timeout, userAgent)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if c, ok := o.clients[localIP]; ok {
		return c
	}
	c := o.NewClient(localIP, timeout, userAgent)
	o.clients[localIP] = c
	return c
}

// proxyFunc resolves the proxy for outgoing requests.
func (o *Outbound) proxyFunc() func(*http.Request) (*url.URL, error) {
	if o.proxy != "" {
		if u, err := url.Parse(o.proxy); err == nil {
			return http.ProxyURL(u)
		}
	}
	return http.ProxyFromEnvironment
}

// dialContext binds new connections to localIP. Loopback destinations are
// left unbound, and a bound dial that cannot be satisfied (an IPv6 source
// with an IPv4-only destination, for example) falls back to the default
// source address so the request still succeeds.
func (o *Outbound) dialContext(localIP string) func(context.Context, string, string) (net.Conn, error) {
	bind := net.ParseIP(localIP)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		src := sourceAddrFor(addr, bind)
		if src == nil {
			return d.DialContext(ctx, network, addr)
		}
		d.LocalAddr = src
		conn, err := d.DialContext(ctx, network, addr)
		if err == nil {
			return conn, nil
		}
		fallback := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
		if conn2, err2 := fallback.DialContext(ctx, network, addr); err2 == nil {
			return conn2, nil
		}
		return nil, err
	}
}

// sourceAddrFor returns the local address to bind a connection to, or nil
// when the connection must use the default source address.
func sourceAddrFor(addr string, bind net.IP) net.Addr {
	if bind == nil || isLoopbackAddr(addr) {
		return nil
	}
	return &net.TCPAddr{IP: bind}
}

// isLoopbackAddr reports whether addr points at the loopback interface.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostPublicIPs lists the host's public unicast addresses, IPv6 first.
// Private addresses (docker bridges, RFC1918, CGNAT) and IPv6 unique-local
// addresses are excluded: they are not usable as the source of an internet
// connection.
func hostPublicIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var v6, v4 []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip == nil {
			continue
		}
		if v4ip := ip.To4(); v4ip != nil {
			if isPublicIPv4(v4ip) {
				v4 = append(v4, v4ip.String())
			}
			continue
		}
		if isPublicIPv6(ip) {
			v6 = append(v6, ip.String())
		}
	}
	return append(v6, v4...)
}

// isPublicIPv4 reports whether ip is a routable public IPv4 address.
func isPublicIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() ||
		ip.IsUnspecified() || ip.IsPrivate() {
		return false
	}
	// Carrier-grade NAT 100.64.0.0/10.
	if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
		return false
	}
	return true
}

// isPublicIPv6 reports whether ip is a globally routable IPv6 address.
func isPublicIPv6(ip net.IP) bool {
	v6 := ip.To16()
	if v6 == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// Global unicast 2000::/3; unique-local fc00::/7 is excluded.
	return v6[0]&0xE0 == 0x20
}

// dedupeStrings removes empty and repeated entries, preserving order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
