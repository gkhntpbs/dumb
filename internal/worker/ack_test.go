package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
)

type genFunc func(ctx context.Context, r ollama.GenerateRequest) (string, error)

func (f genFunc) Generate(ctx context.Context, r ollama.GenerateRequest) (string, error) {
	return f(ctx, r)
}

// slowOllama answers after d.
type slowOllama struct{ d time.Duration }

func (s slowOllama) Generate(ctx context.Context, _ ollama.GenerateRequest) (string, error) {
	select {
	case <-time.After(s.d):
		return `{"ok":true}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func ackServer(t *testing.T, window time.Duration, wrap func(http.Handler) http.Handler) (*httptest.Server, *server.Server) {
	t.Helper()
	tokens := auth.NewStaticTokens()
	tokens.Add("w1", auth.RoleWorker, workerToken)
	tokens.Add("app", auth.RoleCaller, callerToken)
	s, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: time.Second, AckWindow: window})
	if err != nil {
		t.Fatal(err)
	}
	var h http.Handler = s
	if wrap != nil {
		h = wrap(s)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, s
}

// TestWorkerAcksAssignments: a job that runs longer than the ack window still
// completes, because the worker acked it (without the ack the lease would be
// revoked and the result refused).
func TestWorkerAcksAssignments(t *testing.T) {
	var acks atomic.Int32
	var features atomic.Value
	ts, _ := ackServer(t, 50*time.Millisecond, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/workers/hello" {
				var hr protocol.HelloRequest
				b, _ := io.ReadAll(r.Body)
				json.Unmarshal(b, &hr)
				features.Store(hr.Features)
				r.Body = io.NopCloser(bytes.NewReader(b))
			}
			if r.Method == "POST" && len(r.URL.Path) > 4 && r.URL.Path[len(r.URL.Path)-4:] == "/ack" {
				acks.Add(1)
			}
			h.ServeHTTP(w, r)
		})
	})
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "0.1.0", Ollama: slowOllama{300 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	stop := start(t, w)
	defer stop()
	if code, st := submit(t, ts.URL, model); code != 200 || st.State != protocol.StateDone {
		t.Fatalf("slow job: %d %+v", code, st)
	}
	if acks.Load() != 1 {
		t.Fatalf("acks %d, want 1", acks.Load())
	}
	if f, _ := features.Load().([]string); !slices.Equal(f, []string{protocol.FeatureAck}) {
		t.Fatalf("hello features %v, want [ack]", f)
	}
}

// TestWorkerDropsRevokedJob: a 409 on the ack (the lease was revoked) stops
// the generation and no result is posted for the dead lease.
func TestWorkerDropsRevokedJob(t *testing.T) {
	var results atomic.Int32
	ts, s := ackServer(t, time.Second, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch p := r.URL.Path; {
			case len(p) > 4 && p[len(p)-4:] == "/ack":
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(protocol.Error{Error: "lease is not valid"})
				return
			case len(p) > 7 && p[len(p)-7:] == "/result":
				results.Add(1)
			}
			h.ServeHTTP(w, r)
		})
	})
	g := &gatedOllama{started: make(chan struct{}, 1), release: make(chan struct{})}
	ended := make(chan struct{})
	gen := genFunc(func(ctx context.Context, r ollama.GenerateRequest) (string, error) {
		defer close(ended)
		return g.Generate(ctx, r)
	})
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "0.1.0", Ollama: gen})
	if err != nil {
		t.Fatal(err)
	}
	stop := start(t, w)
	defer stop()
	eventually(t, "online", func() bool { return s.Status().WorkersOnline == 1 })
	if code, _ := caller(t, "POST", ts.URL+"/v1/jobs", protocol.SubmitRequest{Model: model, Prompt: "hi", DeadlineMS: 5000}); code != http.StatusAccepted {
		t.Fatalf("submit: %d", code)
	}
	<-g.started
	// The generation is cancelled by the refused ack, not released by us.
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("generation kept running after the ack was refused")
	}
	time.Sleep(100 * time.Millisecond)
	if results.Load() != 0 {
		t.Fatalf("posted %d results for a revoked lease", results.Load())
	}
}

// TestGoodbyeOnStop: a worker that stops says goodbye, so the server stops
// counting it at once and callers get 503 instead of waiting.
func TestGoodbyeOnStop(t *testing.T) {
	ts, s := ackServer(t, time.Second, nil)
	stop := start(t, newWorker(t, ts.URL, workerToken, "0.1.0", model))
	if code, _ := submit(t, ts.URL, model); code != 200 {
		t.Fatalf("job: %d", code)
	}
	stop()
	if st := s.Status(); st.WorkersOnline != 0 {
		t.Fatalf("still online after stop: %+v", st)
	}
	code, _ := caller(t, "POST", ts.URL+"/v1/jobs?wait=1s", protocol.SubmitRequest{Model: model, Prompt: "hi", DeadlineMS: 5000})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("submit after stop: %d, want 503", code)
	}
}

// TestGoodbyeOnDrain: the update-restart path (Drain) says goodbye too.
func TestGoodbyeOnDrain(t *testing.T) {
	ts, s := ackServer(t, time.Second, nil)
	w := newWorker(t, ts.URL, workerToken, "0.1.0", model)
	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background()) }()
	eventually(t, "online", func() bool { return s.Status().WorkersOnline == 1 })
	w.Drain()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.WorkersOnline != 0 {
		t.Fatalf("still online after drain: %+v", st)
	}
}
