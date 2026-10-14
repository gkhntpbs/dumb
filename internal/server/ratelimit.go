package server

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate is a token bucket: PerSecond tokens are added each second, up to Burst.
// A zero Rate takes the default; a negative PerSecond turns the limit off.
type Rate struct {
	PerSecond float64
	Burst     float64
}

// Default rates. A worker makes one long-poll per capacity slot every
// max_wait (25s) plus a claim and a result per job, so 50/s per principal
// leaves room for dozens of short jobs a second. Failed authentication is
// limited per client IP to a burst of 10, then one every 2s.
var (
	DefaultWorkerRate = Rate{PerSecond: 50, Burst: 100}
	DefaultCallerRate = Rate{PerSecond: 50, Burst: 100}
	DefaultUnauthRate = Rate{PerSecond: 0.5, Burst: 10}
)

func (r Rate) orDefault(def Rate) Rate {
	if r == (Rate{}) {
		return def
	}
	return r
}

type bucket struct {
	tokens float64
	last   time.Time
}

// limiter keeps one in-memory token bucket per key.
type limiter struct {
	rate    Rate
	now     func() time.Time
	mu      sync.Mutex
	b       map[string]*bucket
	swept   time.Time
	maxKeys int // optional cap; used for unauthenticated pairing attempts only
}

func newLimiter(r Rate, now func() time.Time) *limiter {
	return &limiter{rate: r, now: now, b: map[string]*bucket{}}
}

// refill brings key's bucket up to date and returns it (nil when off).
func (l *limiter) refillLocked(key string, now time.Time) *bucket {
	b := l.b[key]
	if b == nil {
		b = &bucket{tokens: l.rate.Burst, last: now}
		l.b[key] = b
	}
	b.tokens = min(l.rate.Burst, b.tokens+now.Sub(b.last).Seconds()*l.rate.PerSecond)
	b.last = now
	return b
}

// sweepLocked forgets full buckets once a minute so the map stays small.
func (l *limiter) sweepLocked(now time.Time) {
	if now.Sub(l.swept) < time.Minute {
		return
	}
	l.swept = now
	for k, b := range l.b {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate.PerSecond >= l.rate.Burst {
			delete(l.b, k)
		}
	}
}

// retryAfter is how long until one token is available.
func (l *limiter) retryAfter(b *bucket) time.Duration {
	return time.Duration((1 - b.tokens) / l.rate.PerSecond * float64(time.Second))
}

// allow takes one token for key. When none is left it returns false and how
// long to wait.
func (l *limiter) allow(key string) (bool, time.Duration) {
	if l.rate.PerSecond < 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	if l.maxKeys > 0 && l.b[key] == nil && len(l.b) >= l.maxKeys {
		return false, time.Second
	}
	b := l.refillLocked(key, now)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, l.retryAfter(b)
}

// blocked reports, without taking a token, whether key's bucket is empty.
func (l *limiter) blocked(key string) (bool, time.Duration) {
	if l.rate.PerSecond < 0 {
		return false, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refillLocked(key, l.now())
	if b.tokens >= 1 {
		return false, 0
	}
	return true, l.retryAfter(b)
}

// retryAfterSeconds formats d for the Retry-After header: whole seconds, at least 1.
func retryAfterSeconds(d time.Duration) string {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return strconv.Itoa(s)
}

// clientIP is the address that unauthenticated requests are limited by. The
// TCP peer is used unless it is a trusted proxy; then X-Forwarded-For is read
// from the right, skipping trusted proxies, so a client cannot pick its own
// key by sending the header itself.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !isTrusted(peer, trusted) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !isTrusted(a, trusted) {
			return a.String()
		}
		peer = a
	}
	return peer.String()
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
