package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

var apiRoutes = [...]string{"POST /v1/workers/hello", "POST /v1/workers/goodbye", "GET /v1/workers/me", "GET /v1/jobs/next", "POST /v1/jobs/{id}/ack", "POST /v1/jobs/{id}/result", "POST /v1/jobs", "GET /v1/jobs/{id}", "GET /v1/status", "other"}

func apiRoute(r *http.Request) int {
	path := r.URL.Path
	for i, route := range apiRoutes[:9] {
		method, template, _ := strings.Cut(route, " ")
		if method != r.Method {
			continue
		}
		if template == path {
			return i
		}
		if strings.Contains(template, "{id}") {
			prefix, suffix, _ := strings.Cut(template, "{id}")
			if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, suffix) {
				id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
				if id != "" && !strings.Contains(id, "/") {
					return i
				}
			}
		}
	}
	return 9
}

type apiBucket struct {
	tick                                                             int64
	count, success, redirects, client, server, unauthorized, limited uint64
	latency                                                          time.Duration
}
type apiRouteMetrics struct {
	total    uint64
	inflight int
	buckets  [30]apiBucket
}

// The fixed route table and ring buckets bound storage independently of traffic.
type apiMetrics struct {
	mu     sync.Mutex
	routes [10]apiRouteMetrics
}

func (m *apiMetrics) begin(route int) { m.mu.Lock(); defer m.mu.Unlock(); m.routes[route].inflight++ }
func (m *apiMetrics) end(route int, now time.Time, code int, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &m.routes[route]
	r.inflight--
	r.total++
	tick := now.Unix() / 10
	idx := int((tick%30 + 30) % 30)
	b := &r.buckets[idx]
	if b.tick != tick {
		*b = apiBucket{tick: tick}
	}
	b.count++
	b.latency += latency
	switch {
	case code >= 500:
		b.server++
	case code >= 400:
		b.client++
	case code >= 300:
		b.redirects++
	case code >= 200:
		b.success++
	}
	if code == 401 || code == 403 {
		b.unauthorized++
	}
	if code == 429 {
		b.limited++
	}
}
func (m *apiMetrics) snapshot(now, epoch time.Time) protocol.AdminApi {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := protocol.AdminApi{SnapshotAt: now, Epoch: epoch, UptimeSeconds: max(0, now.Sub(epoch).Seconds()), Status: "serving", WindowSeconds: 300, Routes: []protocol.AdminApiRoute{}, History: []protocol.AdminApiBucket{}}
	tick := now.Unix() / 10
	var totalLatency time.Duration
	firstTick := max(tick-29, epoch.Unix()/10)
	for t := firstTick; t <= tick; t++ {
		out.History = append(out.History, protocol.AdminApiBucket{At: time.Unix(t*10, 0).UTC()})
	}
	for i, r := range m.routes {
		row := protocol.AdminApiRoute{Route: apiRoutes[i], AdminApiCounters: protocol.AdminApiCounters{TotalRequests: r.total, Inflight: r.inflight}}
		var latency time.Duration
		for _, b := range r.buckets {
			if b.count == 0 || b.tick < firstTick || b.tick > tick {
				continue
			}
			row.WindowRequests += b.count
			row.Success += b.success
			row.Redirects += b.redirects
			row.ClientErrors += b.client
			row.ServerErrors += b.server
			row.Unauthorized += b.unauthorized
			row.RateLimited += b.limited
			latency += b.latency
			h := &out.History[b.tick-firstTick]
			h.Requests += b.count
			h.ClientErrors += b.client
			h.ServerErrors += b.server
			h.RateLimited += b.limited
		}
		if row.WindowRequests > 0 {
			mean := float64(latency) / float64(time.Millisecond) / float64(row.WindowRequests)
			row.MeanLatencyMS = &mean
		}
		out.Routes = append(out.Routes, row)
		out.TotalRequests += row.TotalRequests
		out.Inflight += row.Inflight
		out.WindowRequests += row.WindowRequests
		out.Success += row.Success
		out.Redirects += row.Redirects
		out.ClientErrors += row.ClientErrors
		out.ServerErrors += row.ServerErrors
		out.Unauthorized += row.Unauthorized
		out.RateLimited += row.RateLimited
		totalLatency += latency
	}
	if out.WindowRequests > 0 {
		mean := float64(totalLatency) / float64(time.Millisecond) / float64(out.WindowRequests)
		out.MeanLatencyMS = &mean
	}
	duration := min(out.UptimeSeconds, now.Sub(time.Unix((tick-29)*10, 0)).Seconds())
	if duration > 0 {
		out.RequestsPerSecond = float64(out.WindowRequests) / duration
	}
	return out
}
