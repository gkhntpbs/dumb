package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// tagsOllama is a fake Ollama whose model list (/api/tags) can change.
type tagsOllama struct {
	mu     sync.Mutex
	models []string
}

func (f *tagsOllama) set(m ...string) {
	f.mu.Lock()
	f.models = m
	f.mu.Unlock()
}

func (f *tagsOllama) start(t *testing.T) *ollama.Client {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			f.mu.Lock()
			var ms []map[string]string
			for _, m := range f.models {
				ms = append(ms, map[string]string{"name": m})
			}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"models": ms})
		case "/api/generate":
			json.NewEncoder(w).Encode(map[string]any{"response": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	c, err := ollama.New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func caller(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Authorization", "Bearer "+callerToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 300 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestReHelloOnModelChange: the worker announces the configured models Ollama
// actually has, says hello again when that set changes, and stops claiming
// while none is present.
func TestReHelloOnModelChange(t *testing.T) {
	const other = "other:1b"
	var hellos, polling atomic.Int32
	ts := newServer(t, "0.1.0", func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/workers/hello":
				hellos.Add(1)
			case "/v1/jobs/next":
				polling.Add(1)
				defer polling.Add(-1)
			}
			h.ServeHTTP(w, r)
		})
	})
	fo := &tagsOllama{}
	fo.set(model, "unrelated:7b")
	logs := &lockedBuf{}
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model, other}, Capacity: 2,
		Version: "0.1.0", Ollama: fo.start(t), ModelCheck: 20 * time.Millisecond, Logger: log.New(logs, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	stop := start(t, w)
	defer stop()

	status := func() []string {
		_, b := caller(t, "GET", ts.URL+"/v1/status", nil)
		var st protocol.Status
		json.Unmarshal(b, &st)
		return st.Models
	}
	eventually(t, "hello with the present model only", func() bool { return slices.Equal(status(), []string{model}) })
	if hellos.Load() != 1 {
		t.Fatalf("hellos %d, want 1", hellos.Load())
	}
	time.Sleep(100 * time.Millisecond) // unchanged list: no new hello
	if hellos.Load() != 1 {
		t.Fatalf("hello repeated without a change: %d", hellos.Load())
	}

	fo.set(model, other)
	eventually(t, "re-hello with both models", func() bool { return slices.Equal(status(), []string{other, model}) }) // status is sorted
	if hellos.Load() != 2 {
		t.Fatalf("hellos %d, want 2", hellos.Load())
	}

	// No configured model left: the polls stop and the reason is logged.
	fo.set("unrelated:7b")
	eventually(t, "polls to stop", func() bool {
		return polling.Load() == 0 && strings.Contains(logs.String(), "no configured model is present")
	})
	// It said goodbye: callers get 503 at once instead of waiting.
	eventually(t, "worker offline", func() bool { return len(status()) == 0 })
	code, b := caller(t, "POST", ts.URL+"/v1/jobs?wait=200ms", protocol.SubmitRequest{Model: model, Prompt: "hi", DeadlineMS: 5000})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("submit while no model was present: %d %s, want 503", code, b)
	}

	// The model comes back: hello again, and jobs run.
	fo.set(model)
	if code, st := submit(t, ts.URL, model); code != 200 || st.State != protocol.StateDone {
		t.Fatalf("after the model came back: %d %+v", code, st)
	}
	if hellos.Load() != 3 || !slices.Equal(status(), []string{model}) {
		t.Fatalf("hellos %d, status %v", hellos.Load(), status())
	}
}

// TestNoModelAtStartWaits: with none of the configured models in Ollama the
// worker neither says hello nor polls until one appears.
func TestNoModelAtStartWaits(t *testing.T) {
	var hellos, polls atomic.Int32
	ts := newServer(t, "0.1.0", func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/workers/hello":
				hellos.Add(1)
			case "/v1/jobs/next":
				polls.Add(1)
			}
			h.ServeHTTP(w, r)
		})
	})
	fo := &tagsOllama{}
	logs := &lockedBuf{}
	var checkedIn atomic.Int32
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "0.1.0",
		Ollama: fo.start(t), ModelCheck: 20 * time.Millisecond, Logger: log.New(logs, "", 0),
		OnHello: func() { checkedIn.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	time.Sleep(150 * time.Millisecond)
	if hellos.Load() != 0 || polls.Load() != 0 || !strings.Contains(logs.String(), "no configured model is present") {
		t.Fatalf("hellos %d polls %d, log %q", hellos.Load(), polls.Load(), logs.String())
	}
	// It still checks in (GET /v1/workers/me), which proves the server is reachable for probation.
	if checkedIn.Load() == 0 {
		t.Fatal("no server check-in while idle")
	}
	fo.set(model+"x", model) // "smollm2:135m" present
	if code, st := submit(t, ts.URL, model); code != 200 || st.State != protocol.StateDone {
		t.Fatalf("after the model appeared: %d %+v", code, st)
	}
	cancel()
	<-done
}

// TestNoModelStillLearnsUpgradeRequired: an idle worker (no model present)
// still finds out that it is outdated, so the self-updater can run.
func TestNoModelStillLearnsUpgradeRequired(t *testing.T) {
	ts := newServer(t, "2.0.0", nil)
	fo := &tagsOllama{}
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "1.0.0",
		Ollama: fo.start(t), ModelCheck: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var up *ErrUpgradeRequired
	if err := w.Run(context.Background()); !errors.As(err, &up) || up.MinVersion != "2.0.0" {
		t.Fatalf("got %v, want ErrUpgradeRequired{2.0.0}", err)
	}
}
