package ollama

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Remote mode: an explicit opt-in (dumb-worker -ollama-remote) that lets the
// worker use an Ollama on another machine of the same private network instead
// of the loopback one. The worker never manages a remote Ollama: no install,
// start, stop or model pull. See the workspace ADR on remote LAN Ollama.

// ErrNotPrivate means a remote Ollama host is, or resolves to, an address
// outside the private ranges.
var ErrNotPrivate = errors.New("ollama: remote host is not on a private network")

// ErrRemoteManaged means an install, start or pull was asked of a remote Ollama.
var ErrRemoteManaged = errors.New("ollama: a remote Ollama is not managed by this worker; manage it on its own host")

// ErrRemoteUnavailable means the remote Ollama did not answer.
var ErrRemoteUnavailable = errors.New("ollama: remote Ollama unreachable")

// ErrRemoteModelMissing means a configured model is absent on the remote Ollama.
var ErrRemoteModelMissing = errors.New("ollama: configured model missing on the remote Ollama (pull it there)")

var privatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),     // RFC 1918
	netip.MustParsePrefix("172.16.0.0/12"),  // RFC 1918
	netip.MustParsePrefix("192.168.0.0/16"), // RFC 1918
	netip.MustParsePrefix("fc00::/7"),       // RFC 4193 unique local
}

// IsPrivateAddr reports whether a is in a private range: IPv4 10/8,
// 172.16/12, 192.168/16 or IPv6 unique local fc00::/7 (IPv4-mapped IPv6
// counts as its IPv4 address). Loopback, link-local, unspecified, multicast,
// shared (100.64/10) and public addresses are not private. Link-local is
// refused on purpose: it needs an interface zone and is self-assigned, so it
// says nothing about which network the peer is on.
func IsPrivateAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	for _, p := range privatePrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolver looks host names up; *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Open returns a client for baseURL. Without remote, only loopback is
// accepted (exactly as New). With remote, only a host on a private network
// is accepted, and every connection is checked again when it is dialed.
func Open(baseURL string, remote bool) (*Client, error) {
	if !remote {
		return New(baseURL)
	}
	return openRemote(baseURL, net.DefaultResolver, (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext)
}

// CheckRemoteURL validates a remote-mode URL without resolving or contacting it.
func CheckRemoteURL(baseURL string) (*url.URL, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("ollama: %q: scheme must be http or https", baseURL)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("ollama: %q: no credentials, query or fragment allowed", baseURL)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("ollama: %q has no host", baseURL)
	}
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return nil, fmt.Errorf("ollama: %q is loopback; drop -ollama-remote to use the local Ollama", baseURL)
	}
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Unmap().IsLoopback() {
			return nil, fmt.Errorf("ollama: %q is loopback; drop -ollama-remote to use the local Ollama", baseURL)
		}
		if !IsPrivateAddr(a) {
			return nil, fmt.Errorf("%w: %q (allowed: 10/8, 172.16/12, 192.168/16, fc00::/7)", ErrNotPrivate, baseURL)
		}
	} else if strings.Contains(host, "%") || strings.Contains(host, ":") {
		return nil, fmt.Errorf("%w: %q", ErrNotPrivate, baseURL)
	}
	return u, nil
}

func openRemote(baseURL string, resolver Resolver, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*Client, error) {
	u, err := CheckRemoteURL(baseURL)
	if err != nil {
		return nil, err
	}
	d := &privateDialer{resolver: resolver, dial: dial}
	tr := &http.Transport{
		Proxy:                 nil, // a proxy would bypass the address check
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	c, err := newClient(u, tr)
	if err != nil {
		return nil, err
	}
	c.remote = true
	return c, nil
}

// privateDialer resolves the host itself and dials only validated private
// addresses, so a name cannot be rebound to a public or loopback address
// between the check and the connection.
type privateDialer struct {
	resolver Resolver
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d *privateDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	p, err := netip.ParseAddr(host)
	var addrs []netip.Addr
	if err == nil {
		addrs = []netip.Addr{p}
	} else {
		addrs, err = d.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("ollama: %s resolves to no address", host)
	}
	// Every answer must be private: a mixed answer is refused outright.
	for _, a := range addrs {
		if !IsPrivateAddr(a) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrNotPrivate, host, a)
		}
	}
	var last error
	for _, a := range addrs {
		conn, err := d.dial(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}
