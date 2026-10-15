package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
)

// helloBodies runs a real server and hands every hello body to the test.
func helloBodies(t *testing.T) (*httptest.Server, <-chan map[string]any) {
	t.Helper()
	tokens := auth.NewStaticTokens()
	tokens.Add("w1", auth.RoleWorker, workerToken)
	s, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	bodies := make(chan map[string]any, 10)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workers/hello" {
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				bodies <- m
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, bodies
}

func firstHello(t *testing.T, cfg Config) map[string]any {
	t.Helper()
	ts, bodies := helloBodies(t)
	cfg.ServerURL, cfg.Token, cfg.Models, cfg.Version, cfg.Ollama = ts.URL, workerToken, []string{model}, "0.1.0", fakeOllama(t)
	w, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	select {
	case m := <-bodies:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
		return nil
	}
}

func TestHelloSendsHostnameByDefault(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname on this machine:", err)
	}
	want := protocol.NormalizeWorkerName(host)
	got, ok := firstHello(t, Config{})["worker_name"]
	if want == "" {
		if ok {
			t.Fatalf("unusable hostname sent as %v", got)
		}
		return
	}
	if got != want {
		t.Fatalf("worker_name %v, want hostname %q", got, want)
	}
}

func TestHelloSendsConfiguredName(t *testing.T) {
	if got := firstHello(t, Config{Name: "  Lab Pi 5 ç "})["worker_name"]; got != "Lab Pi 5 ç" {
		t.Fatalf("worker_name %v", got)
	}
}

func TestInvalidConfiguredNameRefused(t *testing.T) {
	for _, name := range []string{"   ", "two\nlines", "esc\x1b[0m"} {
		_, err := New(Config{ServerURL: "http://127.0.0.1:1", Token: workerToken, Models: []string{model}, Version: "0.1.0", Ollama: fakeOllama(t), Name: name})
		if err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
}
