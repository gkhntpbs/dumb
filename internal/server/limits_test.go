package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func (e *env) helloCap(capacity int) {
	e.t.Helper()
	res, b := e.do("POST", "/v1/workers/hello", workerToken, "1.2.0", protocol.HelloRequest{
		WorkerVersion: "1.2.0", ProtocolVersion: protocol.Version, Models: []string{model}, Capacity: capacity,
	})
	if res.StatusCode != 200 {
		e.t.Fatalf("hello: %d %s", res.StatusCode, b)
	}
}

func (e *env) claim(wait string) (int, protocol.Assignment) {
	e.t.Helper()
	res, b := e.do("GET", "/v1/jobs/next?wait="+wait, workerToken, "1.2.0", nil)
	var a protocol.Assignment
	json.Unmarshal(b, &a)
	return res.StatusCode, a
}

// TestCapacityEnforced: a worker that announced capacity 2 never holds a third
// lease; the extra poll waits and gets the job the moment a lease ends.
func TestCapacityEnforced(t *testing.T) {
	e := newEnv(t)
	e.helloCap(2)
	for range 3 {
		e.do("POST", "/v1/jobs", callerToken, "", submitReq(5000))
	}
	c1, a1 := e.claim("0s")
	c2, _ := e.claim("0s")
	if c1 != 200 || c2 != 200 {
		t.Fatalf("claims within capacity: %d %d", c1, c2)
	}
	if c, _ := e.claim("100ms"); c != http.StatusNoContent {
		t.Fatalf("claim beyond capacity: got %d, want 204", c)
	}
	got := make(chan time.Time, 1)
	go func() {
		if c, _ := e.claim("2s"); c == 200 {
			got <- time.Now()
		}
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if res, _ := e.do("POST", "/v1/jobs/"+a1.JobID+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a1.LeaseID, Output: "x"}); res.StatusCode != 204 {
		t.Fatalf("result: %d", res.StatusCode)
	}
	select {
	case at := <-got:
		if d := at.Sub(start); d > 50*time.Millisecond {
			t.Fatalf("capacity wait added %v, want < 50ms", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting poll never got the job after a lease ended")
	}
	if n := e.srv.q.Leases("w1"); n != 2 {
		t.Fatalf("live leases %d, want 2", n)
	}
}

// TestAbortedPollRequeuesJob: the poll's context is cancelled in the instant
// its job is claimed; the job must go back to the front of the queue.
func TestAbortedPollRequeuesJob(t *testing.T) {
	e := newEnv(t)
	e.helloCap(1)
	_, b := e.do("POST", "/v1/jobs", callerToken, "", submitReq(5000))
	var first protocol.JobStatus
	json.Unmarshal(b, &first)
	e.do("POST", "/v1/jobs", callerToken, "", submitReq(5000))

	ctx, cancel := context.WithCancel(context.Background())
	e.srv.afterClaim = func(*http.Request) { cancel() }
	req := httptest.NewRequestWithContext(ctx, "GET", "/v1/jobs/next?wait=1s", nil)
	req.Header.Set("Authorization", "Bearer "+workerToken)
	req.Header.Set(protocol.WorkerVersionHeader, "1.2.0")
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	e.srv.afterClaim = nil
	if rec.Body.Len() > 0 {
		t.Fatalf("aborted poll was answered with the job: %s", rec.Body)
	}
	if n := e.srv.q.Leases("w1"); n != 0 {
		t.Fatalf("lease kept by nobody: %d live leases", n)
	}
	if e.srv.q.Pending() != 2 {
		t.Fatalf("pending %d, want 2", e.srv.q.Pending())
	}
	c, a := e.claim("0s")
	if c != 200 || a.JobID != first.JobID {
		t.Fatalf("next claim: %d %s, want the requeued job %s first", c, a.JobID, first.JobID)
	}
}

func TestWorkerAndCallerRateLimits(t *testing.T) {
	e := newEnvWith(t, func(c *Config) {
		c.WorkerRate = Rate{PerSecond: 1, Burst: 3}
		c.CallerRate = Rate{PerSecond: 0.5, Burst: 2}
	})
	for i := range 3 {
		if res, _ := e.do("GET", "/v1/workers/me", workerToken, "1.2.0", nil); res.StatusCode != 200 {
			t.Fatalf("worker request %d: %d", i, res.StatusCode)
		}
	}
	res, b := e.do("GET", "/v1/workers/me", workerToken, "1.2.0", nil)
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") != "1" || !strings.Contains(string(b), "rate limit") {
		t.Fatalf("worker over limit: %d Retry-After=%q %s", res.StatusCode, res.Header.Get("Retry-After"), b)
	}
	// The caller's bucket is separate from the worker's.
	for range 2 {
		if res, _ := e.do("GET", "/v1/status", callerToken, "", nil); res.StatusCode != 200 {
			t.Fatalf("caller: %d", res.StatusCode)
		}
	}
	res, _ = e.do("GET", "/v1/status", callerToken, "", nil)
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") != "2" {
		t.Fatalf("caller over limit: %d Retry-After=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	time.Sleep(1100 * time.Millisecond) // one worker token refills
	if res, _ := e.do("GET", "/v1/workers/me", workerToken, "1.2.0", nil); res.StatusCode != 200 {
		t.Fatalf("after refill: %d", res.StatusCode)
	}
}

// TestDefaultsDoNotThrottleLongPolling: a capacity-8 worker's normal traffic
// (eight long-polls plus a burst of quick claims and results) stays under the default.
func TestDefaultsDoNotThrottleLongPolling(t *testing.T) {
	e := newEnv(t)
	e.helloCap(8)
	for i := range 90 {
		if c, _ := e.claim("0s"); c != http.StatusNoContent {
			t.Fatalf("poll %d: %d", i, c)
		}
	}
}

func TestUnauthenticatedLimitedPerIP(t *testing.T) {
	e := newEnvWith(t, func(c *Config) { c.UnauthRate = Rate{PerSecond: 0.1, Burst: 3} })
	for i := range 3 {
		if res, _ := e.do("GET", "/v1/status", "guess-"+strconv.Itoa(i), "", nil); res.StatusCode != 401 {
			t.Fatalf("guess %d: %d", i, res.StatusCode)
		}
	}
	// Now even a right token from that IP is refused before it is checked.
	res, _ := e.do("GET", "/v1/status", callerToken, "", nil)
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") != "10" {
		t.Fatalf("after 3 failures: %d Retry-After=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	// X-Forwarded-For is ignored without a trusted proxy: a forged header does not get a fresh bucket.
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer guess")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("forged X-Forwarded-For escaped the limit: %d", r2.StatusCode)
	}
}

func TestClientIP(t *testing.T) {
	proxy := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		remote, xff string
		trusted     []netip.Prefix
		want        string
	}{
		{"192.0.2.1:5000", "203.0.113.9", nil, "192.0.2.1"},
		{"10.0.0.2:5000", "203.0.113.9", nil, "10.0.0.2"},
		{"10.0.0.2:5000", "203.0.113.9", proxy, "203.0.113.9"},
		{"10.0.0.2:5000", "198.51.100.1, 203.0.113.9, 10.0.0.3", proxy, "203.0.113.9"},
		{"192.0.2.1:5000", "203.0.113.9", proxy, "192.0.2.1"},
		{"10.0.0.2:5000", "", proxy, "10.0.0.2"},
		{"10.0.0.2:5000", "garbage", proxy, "10.0.0.2"},
		{"[::ffff:192.0.2.1]:5000", "", nil, "192.0.2.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r, c.trusted); got != c.want {
			t.Errorf("%s / %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestLimiterSweepsFullBuckets(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(Rate{PerSecond: 1, Burst: 2}, func() time.Time { return now })
	for i := range 100 {
		l.allow(strconv.Itoa(i))
	}
	now = now.Add(2 * time.Minute)
	l.allow("x")
	if len(l.b) != 1 {
		t.Fatalf("%d buckets kept, want 1", len(l.b))
	}
	if ok, _ := (newLimiter(Rate{PerSecond: -1}, time.Now)).allow("k"); !ok {
		t.Fatal("negative rate must disable the limit")
	}
}
