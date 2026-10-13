package server

import (
	"encoding/json"
	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAdminApiActualPublicTraffic(t *testing.T) {
	a := auth.NewStaticTokens()
	caller, admin := randomAdminToken(), randomAdminToken()
	a.Add("caller", auth.RoleCaller, caller)
	a.Add("admin", auth.RoleAdminRead, admin)
	now := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	s, err := New(Config{Auth: a, MinWorkerVersion: "0.0.0", Now: func() time.Time { return now }, CallerRate: Rate{PerSecond: 0.001, Burst: 1}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, token string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if request("/v1/status", caller) != 200 {
		t.Fatal("status")
	}
	if request("/v1/status", caller) != 429 {
		t.Fatal("rate limit")
	}
	if request("/v1/status?token=never-retain", "") != 401 {
		t.Fatal("authentication")
	}
	if request("/unknown-private-path", "") != 404 {
		t.Fatal("unknown")
	}
	w := adminGet(s, "/admin/v1/api", admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got protocol.AdminApi
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TotalRequests != 4 || got.WindowRequests != 4 || got.Success != 1 || got.ClientErrors != 3 || got.Unauthorized != 1 || got.RateLimited != 1 || got.Inflight != 0 || len(got.History) != 1 || got.MeanLatencyMS == nil {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(w.Body.String(), "never-retain") || strings.Contains(w.Body.String(), "unknown-private-path") || strings.Contains(w.Body.String(), caller) {
		t.Fatal("unsafe metadata")
	}
	if adminGet(s, "/admin/v1/api", caller).Code != 401 || adminGet(s, "/admin/v1/api", "").Code != 401 {
		t.Fatal("admin auth")
	}
	if s.api.snapshot(now, s.epoch).TotalRequests != 4 {
		t.Fatal("admin counted")
	}
	now = now.Add(301 * time.Second)
	got = s.api.snapshot(now, s.epoch)
	if got.TotalRequests != 4 || got.WindowRequests != 0 || got.MeanLatencyMS != nil || got.RequestsPerSecond != 0 {
		t.Fatalf("expiry %+v", got)
	}
}
func TestApiMetricsInflightBoundariesAndConcurrency(t *testing.T) {
	var m apiMetrics
	epoch := time.Unix(1000, 0)
	now := epoch.Add(100 * time.Second)
	m.begin(3)
	if got := m.snapshot(now, epoch); got.Inflight != 1 || got.ServerErrors != 0 || got.WindowRequests != 0 || got.Status != "serving" {
		t.Fatal(got)
	}
	m.end(3, now, 204, 25*time.Second)
	got := m.snapshot(now, epoch)
	if *got.MeanLatencyMS != 25000 || got.Success != 1 || got.Inflight != 0 {
		t.Fatal(got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				m.begin(9)
				m.end(9, now, 503, time.Millisecond)
				_ = m.snapshot(now, epoch)
			}
		})
	}
	wg.Wait()
	got = m.snapshot(now, epoch)
	if got.TotalRequests != 2001 || got.ServerErrors != 2000 || got.Inflight != 0 || len(got.History) != 11 || len(got.Routes) != 10 {
		t.Fatal(got)
	}
}

func TestApiMetricsTracksLivePublicHandler(t *testing.T) {
	s, _, _ := adminFixture(t)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.mux.HandleFunc("GET /test-wait", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	go func() {
		defer close(finished)
		s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/test-wait", nil))
	}()
	<-entered
	got := s.api.snapshot(s.cfg.Now(), s.epoch)
	if got.Inflight != 1 || got.TotalRequests != 0 || got.ServerErrors != 0 {
		t.Errorf("active request: %+v", got)
	}
	close(release)
	<-finished
	got = s.api.snapshot(s.cfg.Now(), s.epoch)
	if got.Inflight != 0 || got.TotalRequests != 1 || got.Success != 1 {
		t.Fatalf("completed request: %+v", got)
	}
}
