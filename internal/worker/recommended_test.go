package worker

import (
	"context"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/server"
	"net/http/httptest"
)

func recommendingServer(t *testing.T, recommended string) *httptest.Server {
	t.Helper()
	tokens := auth.NewStaticTokens()
	tokens.Add("w1", auth.RoleWorker, workerToken)
	s, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", RecommendedWorkerVersion: recommended, MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

// The recommendation reaches the supervisor from hello and from the
// model-less server check alike.
func TestRecommendedVersionReported(t *testing.T) {
	for name, models := range map[string][]string{"hello": {model}, "check-in": {"absent:1b"}} {
		t.Run(name, func(t *testing.T) {
			ts := recommendingServer(t, "0.3.0")
			got := make(chan string, 10)
			w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: models, Version: "0.1.0",
				Ollama: fakeOllama(t), OnRecommended: func(v string) { got <- v }})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go w.Run(ctx)
			select {
			case v := <-got:
				if v != "0.3.0" {
					t.Fatalf("recommended %q, want 0.3.0", v)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no recommendation reported")
			}
		})
	}
}

func TestNoRecommendationNotReported(t *testing.T) {
	ts := recommendingServer(t, "")
	got := make(chan string, 10)
	hello := make(chan struct{}, 10)
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "0.1.0",
		Ollama: fakeOllama(t), OnRecommended: func(v string) { got <- v }, OnHello: func() { hello <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	select {
	case <-hello:
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	select {
	case v := <-got:
		t.Fatalf("recommendation %q reported without one configured", v)
	default:
	}
}
