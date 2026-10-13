package protocol

import "time"

// AdminApiCounters contains process-lifetime totals and rolling-window outcomes.
type AdminApiCounters struct {
	TotalRequests  uint64   `json:"total_requests"`
	WindowRequests uint64   `json:"window_requests"`
	Inflight       int      `json:"inflight"`
	Success        uint64   `json:"success"`
	Redirects      uint64   `json:"redirects"`
	ClientErrors   uint64   `json:"client_errors"`
	ServerErrors   uint64   `json:"server_errors"`
	Unauthorized   uint64   `json:"unauthorized"`
	RateLimited    uint64   `json:"rate_limited"`
	MeanLatencyMS  *float64 `json:"mean_latency_ms"`
}
type AdminApiRoute struct {
	AdminApiCounters
	Route string `json:"route"`
}
type AdminApiBucket struct {
	At           time.Time `json:"at"`
	Requests     uint64    `json:"requests"`
	ClientErrors uint64    `json:"client_errors"`
	ServerErrors uint64    `json:"server_errors"`
	RateLimited  uint64    `json:"rate_limited"`
}
type AdminApi struct {
	AdminApiCounters
	SnapshotAt        time.Time        `json:"snapshot_at"`
	Epoch             time.Time        `json:"epoch"`
	UptimeSeconds     float64          `json:"uptime_seconds"`
	Status            string           `json:"status"`
	WindowSeconds     int              `json:"window_seconds"`
	RequestsPerSecond float64          `json:"requests_per_second"`
	Routes            []AdminApiRoute  `json:"routes"`
	History           []AdminApiBucket `json:"history"`
}
