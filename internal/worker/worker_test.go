package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
)

const (
	workerToken = "test-worker-token"
	callerToken = "test-caller-token"
	model       = "smollm2:135m"
)

// fakeOllama answers /api/generate; the model "broken" fails.
func fakeOllama(t *testing.T) *ollama.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] == "broken" {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"model crashed"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"response": `{"echo":"` + req["prompt"].(string) + `"}`})
	}))
	t.Cleanup(ts.Close)
	c, err := ollama.New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// newServer starts a real dumb-server; wrap (optional) sits in front of it.
func newServer(t *testing.T, minVersion string, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	tokens := auth.NewStaticTokens()
	tokens.Add("w1", auth.RoleWorker, workerToken)
	tokens.Add("app", auth.RoleCaller, callerToken)
	s, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: minVersion, MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var h http.Handler = s
	if wrap != nil {
		h = wrap(s)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func newWorker(t *testing.T, url, token, version string, models ...string) *Worker {
	t.Helper()
	w, err := New(Config{ServerURL: url, Token: token, Models: models, Capacity: 2, Version: version, Ollama: fakeOllama(t)})
	if err != nil {
		t.Fatal(err)
	}
	w.backoff, w.maxBack = time.Millisecond, 5*time.Millisecond
	return w
}

// start runs w in the background; the returned func stops it and returns Run's error.
func start(t *testing.T, w *Worker) func() error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not stop")
			return nil
		}
	}
}

func submit(t *testing.T, base, m string) (int, protocol.JobStatus) {
	t.Helper()
	b, _ := json.Marshal(protocol.SubmitRequest{Model: m, Prompt: "hi", Format: "json", DeadlineMS: 5000})
	// The worker may not have said hello yet; retry the 503 briefly.
	for range 100 {
		req, _ := http.NewRequest("POST", base+"/v1/jobs?wait=3s", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+callerToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusServiceUnavailable {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		var st protocol.JobStatus
		json.Unmarshal(body, &st)
		return res.StatusCode, st
	}
	t.Fatal("no worker came online")
	return 0, protocol.JobStatus{}
}

func TestEndToEnd(t *testing.T) {
	ts := newServer(t, "0.1.0", nil)
	stop := start(t, newWorker(t, ts.URL, workerToken, "0.1.0", model, "broken"))
	code, st := submit(t, ts.URL, model)
	if code != 200 || st.State != protocol.StateDone || st.Output != `{"echo":"hi"}` {
		t.Fatalf("done job: %d %+v", code, st)
	}
	code, st = submit(t, ts.URL, "broken")
	if code != 200 || st.State != protocol.StateFailed || !strings.Contains(st.Error, "model crashed") {
		t.Fatalf("failed job: %d %+v", code, st)
	}
	if err := stop(); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop: %v", err)
	}
}

func TestUpgradeRequiredStops(t *testing.T) {
	ts := newServer(t, "2.0.0", nil)
	err := newWorker(t, ts.URL, workerToken, "1.9.9", model).Run(context.Background())
	var up *ErrUpgradeRequired
	if !errors.As(err, &up) || up.MinVersion != "2.0.0" {
		t.Fatalf("got %v, want ErrUpgradeRequired{2.0.0}", err)
	}
}

func TestUnauthorizedStops(t *testing.T) {
	ts := newServer(t, "0.1.0", nil)
	if err := newWorker(t, ts.URL, "wrong", "0.1.0", model).Run(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v, want ErrUnauthorized", err)
	}
}

func TestHelloRetriesTransientFailures(t *testing.T) {
	var hellos atomic.Int32
	ts := newServer(t, "0.1.0", func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/workers/hello" && hellos.Add(1) <= 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			h.ServeHTTP(w, r)
		})
	})
	stop := start(t, newWorker(t, ts.URL, workerToken, "0.1.0", model))
	if code, st := submit(t, ts.URL, model); code != 200 || st.State != protocol.StateDone {
		t.Fatalf("after retries: %d %+v", code, st)
	}
	stop()
	if hellos.Load() < 4 {
		t.Errorf("hello attempts %d, want >= 4", hellos.Load())
	}
}

func TestServerURL(t *testing.T) {
	for _, ok := range []string{"https://dumb.example.invalid", "http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:1"} {
		if _, err := CheckServerURL(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://dumb.example.invalid", "http://10.0.0.1:8080", "ftp://127.0.0.1", "dumb.example.invalid", ""} {
		if _, err := CheckServerURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
		if _, err := New(Config{ServerURL: bad, Token: "t", Models: []string{model}, Version: "1.0.0"}); err == nil {
			t.Errorf("New accepted %q", bad)
		}
	}
}

func TestTokenFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	os.WriteFile(p, []byte("  secret-token\n"), 0o600)
	if tok, err := ReadTokenFile(p); err != nil || tok != "secret-token" {
		t.Fatalf("0600: %q %v", tok, err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644} {
		os.Chmod(p, mode)
		if _, err := ReadTokenFile(p); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %v accepted: %v", mode, err)
		}
	}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	if _, err := ReadTokenFile(empty); err == nil {
		t.Error("empty token accepted")
	}
}

// gatedOllama blocks every Generate until release is closed.
type gatedOllama struct {
	started chan struct{}
	release chan struct{}
}

func (g *gatedOllama) Generate(ctx context.Context, r ollama.GenerateRequest) (string, error) {
	g.started <- struct{}{}
	select {
	case <-g.release:
		return `{"ok":true}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestDrainFinishesJobsInFlight(t *testing.T) {
	ts := newServer(t, "0.1.0", nil)
	g := &gatedOllama{started: make(chan struct{}, 1), release: make(chan struct{})}
	var hellos atomic.Int32
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Capacity: 2,
		Version: "0.1.1-beta.1", Ollama: g, OnHello: func() { hellos.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background()) }()
	result := make(chan protocol.JobStatus, 1)
	go func() { _, st := submit(t, ts.URL, model); result <- st }()
	<-g.started // the job is running
	w.Drain()
	select {
	case err := <-done:
		t.Fatalf("Run returned %v with a job in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(g.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after drain")
	}
	if st := <-result; st.State != protocol.StateDone {
		t.Fatalf("drained job: %+v", st)
	}
	if hellos.Load() != 1 {
		t.Errorf("OnHello called %d times, want 1", hellos.Load())
	}
}
