package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

func adminDecode[T any](t *testing.T, s *Server, path, token string) T {
	t.Helper()
	w := adminGet(s, path, token)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body)
	}
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func publicCall(s *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(protocol.WorkerVersionHeader, "1.0.0")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// The admin-read role must not unlock any worker or caller operation.
func TestAdminRoleCannotUsePublicEndpoints(t *testing.T) {
	s, _, v := adminFixture(t)
	for _, c := range []struct{ method, path string }{{"GET", "/v1/status"}, {"POST", "/v1/jobs"}, {"GET", "/v1/workers/me"}, {"GET", "/v1/jobs/next?wait=0"}} {
		if w := publicCall(s, c.method, c.path, v, protocol.SubmitRequest{Model: "m", Prompt: "p", DeadlineMS: 1000}); w.Code != 401 {
			t.Fatalf("%s %s: %d", c.method, c.path, w.Code)
		}
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func clockFixture(t *testing.T) (*Server, *auth.StaticTokens, string, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)}
	a := auth.NewStaticTokens()
	v := randomAdminToken()
	a.Add("admin", auth.RoleAdminRead, v)
	s, err := New(Config{Auth: a, MinWorkerVersion: "0.0.0", OnlineTTL: 30 * time.Second, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, v, clock
}

// Epoch, snapshot time, queue age and per-job wait come from the injected clock.
func TestAdminSnapshotClockAndWait(t *testing.T) {
	s, a, v, clock := clockFixture(t)
	start := clock.Now()
	wt := randomAdminToken()
	a.Add("w1", auth.RoleWorker, wt)
	publicCall(s, "POST", "/v1/workers/hello", wt, protocol.HelloRequest{WorkerVersion: "1.0.0", ProtocolVersion: protocol.Version, Models: []string{"m"}, Capacity: 2})
	first := s.q.Submit(protocol.SubmitRequest{Model: "m", Prompt: "p", DeadlineMS: 600000})
	clock.Advance(3 * time.Second)
	s.q.Submit(protocol.SubmitRequest{Model: "m", Prompt: "p", DeadlineMS: 600000})
	clock.Advance(4 * time.Second)
	sum := adminDecode[protocol.AdminSummary](t, s, "/admin/v1/summary", v)
	if !sum.Epoch.Equal(start) || !sum.SnapshotAt.Equal(start.Add(7*time.Second)) || sum.OldestQueuedMS != 7000 || sum.Queued != 2 {
		t.Fatalf("summary: %+v", sum)
	}
	lease, err := s.q.Claim(context.Background(), "w1", []string{"m"}, 2)
	if err != nil || lease.JobID != first {
		t.Fatal("claim", err)
	}
	clock.Advance(2 * time.Second)
	if err := s.q.Complete(first, lease.LeaseID, "w1", "o", ""); err != nil {
		t.Fatal(err)
	}
	q := adminDecode[protocol.AdminQueue](t, s, "/admin/v1/queue", v)
	waits := map[string]int{}
	for _, j := range q.Items {
		waits[j.State] = *j.WaitMS
	}
	if waits[protocol.StateDone] != 7000 || waits[protocol.StateQueued] != 6000 {
		t.Fatalf("waits: %v", waits)
	}
	sum = adminDecode[protocol.AdminSummary](t, s, "/admin/v1/summary", v)
	if sum.LatencySamples != 1 || *sum.MeanWaitMS != 7000 || *sum.MeanCompletionMS != 9000 {
		t.Fatalf("latency: %+v", sum)
	}
	clock.Advance(301 * time.Second)
	if sum = adminDecode[protocol.AdminSummary](t, s, "/admin/v1/summary", v); sum.LatencySamples != 0 || sum.MeanWaitMS != nil {
		t.Fatalf("window: %+v", sum)
	}
}

// Elapsed deadlines read as expired and a silent worker goes offline after the
// TTL, both on the injected clock; goodbye removes a worker at once.
func TestAdminSnapshotExpiryTTLAndGoodbye(t *testing.T) {
	s, a, v, clock := clockFixture(t)
	wt, wt2 := randomAdminToken(), randomAdminToken()
	a.Add("w1", auth.RoleWorker, wt)
	a.Add("w2", auth.RoleWorker, wt2)
	for _, tok := range []string{wt, wt2} {
		if w := publicCall(s, "POST", "/v1/workers/hello", tok, protocol.HelloRequest{WorkerVersion: "1.0.0", ProtocolVersion: protocol.Version, Models: []string{"m"}, Capacity: 3}); w.Code != 200 {
			t.Fatalf("hello: %d %s", w.Code, w.Body)
		}
	}
	s.q.Submit(protocol.SubmitRequest{Model: "m", Prompt: "x", DeadlineMS: 60000})
	clock.Advance(59 * time.Second)
	if q := adminDecode[protocol.AdminQueue](t, s, "/admin/v1/queue?state=queued", v); q.Total != 1 {
		t.Fatalf("before deadline: %+v", q)
	}
	clock.Advance(time.Second)
	if q := adminDecode[protocol.AdminQueue](t, s, "/admin/v1/queue?state=expired", v); q.Total != 1 || q.Items[0].State != protocol.StateExpired {
		t.Fatalf("expired: %+v", q)
	}
	// Both workers were last seen 60 s ago, past the 30 s TTL.
	sum := adminDecode[protocol.AdminSummary](t, s, "/admin/v1/summary", v)
	if sum.Queued != 0 || sum.WorkersOnline != 0 || sum.FreeSlots != 0 {
		t.Fatalf("after TTL: %+v", sum)
	}
	if ws := adminDecode[protocol.AdminWorkers](t, s, "/admin/v1/workers?state=offline", v); ws.Total != 2 {
		t.Fatalf("offline: %+v", ws)
	}
	publicCall(s, "POST", "/v1/workers/hello", wt, protocol.HelloRequest{WorkerVersion: "1.0.0", ProtocolVersion: protocol.Version, Models: []string{"m"}, Capacity: 3})
	clock.Advance(30 * time.Second)
	if sum = adminDecode[protocol.AdminSummary](t, s, "/admin/v1/summary", v); sum.WorkersOnline != 1 || sum.FreeSlots != 3 {
		t.Fatalf("at TTL edge: %+v", sum)
	}
	if w := publicCall(s, "POST", "/v1/workers/goodbye", wt, nil); w.Code != 204 {
		t.Fatalf("goodbye: %d", w.Code)
	}
	if sum = adminDecode[protocol.AdminSummary](t, s, "/admin/v1/summary", v); sum.WorkersOnline != 0 || sum.Capacity != 0 {
		t.Fatalf("after goodbye: %+v", sum)
	}
	if ws := adminDecode[protocol.AdminWorkers](t, s, "/admin/v1/workers", v); ws.Total != 1 || ws.Items[0].WorkerID != "w2" {
		t.Fatalf("workers after goodbye: %+v", ws)
	}
}

// Pages are bounded, ordered newest first (creation time, then id), and never overlap.
func TestAdminQueuePaginationBounds(t *testing.T) {
	s, _, v := adminFixture(t)
	for range 120 {
		s.q.Submit(protocol.SubmitRequest{Model: "m", Prompt: "p", DeadlineMS: 60000})
	}
	first := adminDecode[protocol.AdminQueue](t, s, "/admin/v1/queue?limit=100", v)
	second := adminDecode[protocol.AdminQueue](t, s, "/admin/v1/queue?limit=100&offset=100", v)
	beyond := adminDecode[protocol.AdminQueue](t, s, "/admin/v1/queue?offset=10000", v)
	if first.Total != 120 || len(first.Items) != 100 || len(second.Items) != 20 || len(beyond.Items) != 0 {
		t.Fatalf("pages: %d %d %d %d", first.Total, len(first.Items), len(second.Items), len(beyond.Items))
	}
	seen := map[string]bool{}
	all := append(first.Items, second.Items...)
	for i, j := range all {
		if seen[j.JobID] {
			t.Fatal("duplicate across pages", j.JobID)
		}
		seen[j.JobID] = true
		if i > 0 && j.CreatedAt.After(all[i-1].CreatedAt) {
			t.Fatal("unordered")
		}
	}
	for _, q := range []string{"limit=0", "limit=abc", "offset=x"} {
		if w := adminGet(s, "/admin/v1/workers?"+q, v); w.Code != 400 {
			t.Fatal(q, w.Code)
		}
	}
}

// Snapshots taken while the queue changes stay internally valid.
func TestAdminConcurrentSnapshots(t *testing.T) {
	s, _, v := adminFixture(t)
	s.workers["w"] = &worker{models: []string{"m"}, version: "1", capacity: 4, lastSeen: time.Now(), gone: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			id := s.q.Submit(protocol.SubmitRequest{Model: "m", Prompt: "p", DeadlineMS: 60000})
			if l, err := s.q.Claim(ctx, "w", []string{"m"}, 4); err == nil && l != nil {
				_ = s.q.Complete(id, l.LeaseID, "w", "o", "")
			}
		}
	}()
	errs := make(chan string, 64)
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 25 {
				for _, p := range []string{"summary", "workers", "queue"} {
					w := adminGet(s, "/admin/v1/"+p, v)
					if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
						errs <- p
						return
					}
				}
				var sum protocol.AdminSummary
				_ = json.Unmarshal(adminGet(s, "/admin/v1/summary", v).Body.Bytes(), &sum)
				if sum.ActiveSlots+sum.FreeSlots != sum.Capacity && sum.ActiveSlots <= sum.Capacity {
					errs <- "slot arithmetic"
					return
				}
			}
		}()
	}
	readers.Wait()
	cancel()
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// Prompts, outputs, raw errors and credentials never reach admin responses or logs.
func TestAdminResponsesAndLogsExcludeSecrets(t *testing.T) {
	var logs bytes.Buffer
	a := auth.NewStaticTokens()
	v, wt, ct, bad := randomAdminToken(), randomAdminToken(), randomAdminToken(), randomAdminToken()
	a.Add("admin", auth.RoleAdminRead, v)
	a.Add("w1", auth.RoleWorker, wt)
	a.Add("c1", auth.RoleCaller, ct)
	s, err := New(Config{Auth: a, MinWorkerVersion: "0.0.0", Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	prompt, output, rawErr := randomAdminToken(), randomAdminToken(), randomAdminToken()
	publicCall(s, "POST", "/v1/workers/hello", wt, protocol.HelloRequest{WorkerVersion: "1.0.0", ProtocolVersion: protocol.Version, Models: []string{"m"}, Capacity: 2})
	for i, result := range []protocol.ResultRequest{{Output: output}, {Error: rawErr}} {
		publicCall(s, "POST", "/v1/jobs", ct, protocol.SubmitRequest{Model: "m", Prompt: prompt, System: prompt, DeadlineMS: 60000})
		var asg protocol.Assignment
		if err := json.Unmarshal(publicCall(s, "GET", "/v1/jobs/next?wait=0", wt, nil).Body.Bytes(), &asg); err != nil {
			t.Fatal(i, err)
		}
		result.LeaseID = asg.LeaseID
		publicCall(s, "POST", "/v1/jobs/"+asg.JobID+"/result", wt, result)
	}
	publicCall(s, "POST", "/v1/jobs", ct, protocol.SubmitRequest{Model: "m", Prompt: prompt, DeadlineMS: 60000})
	var body strings.Builder
	for _, p := range []string{"summary", "workers", "queue", "queue?state=failed", "queue?state=done"} {
		body.WriteString(adminGet(s, "/admin/v1/"+p, v).Body.String())
	}
	adminGet(s, "/admin/v1/summary", bad)
	adminGet(s, "/admin/v1/summary", wt)
	if !strings.Contains(body.String(), `"failed"`) {
		t.Fatal("fixture did not produce a failed job")
	}
	for name, secret := range map[string]string{"prompt": prompt, "output": output, "raw error": rawErr, "admin token": v, "worker token": wt, "caller token": ct, "bad token": bad} {
		if strings.Contains(body.String(), secret) {
			t.Fatal(name, "in admin response")
		}
		if strings.Contains(logs.String(), secret) {
			t.Fatal(name, "in logs")
		}
	}
}
