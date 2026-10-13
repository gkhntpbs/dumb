package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/worker"
)

type desktopReadinessBackend struct {
	available atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (b *desktopReadinessBackend) List(context.Context) ([]string, error) {
	if !b.available.Load() {
		return nil, errors.New("fixture API unavailable")
	}
	return []string{"fixture:model"}, nil
}
func (b *desktopReadinessBackend) Generate(context.Context, ollama.GenerateRequest) (string, error) {
	return "", errors.New("no job expected")
}
func (b *desktopReadinessBackend) Ensure(ctx context.Context) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
		b.available.Store(true)
		return nil
	}
}

func TestDesktopReadinessWaitsForRealHelloAndAvailableModels(t *testing.T) {
	var checks, hellos, ready atomic.Int32
	backend := &desktopReadinessBackend{entered: make(chan struct{}), release: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workers/me":
			checks.Add(1)
			json.NewEncoder(w).Encode(protocol.WhoAmI{WorkerID: "inert-desktop-worker"})
		case "/v1/workers/hello":
			hellos.Add(1)
			json.NewEncoder(w).Encode(protocol.HelloResponse{WorkerID: "inert-desktop-worker", MaxWait: "1s"})
		case "/v1/jobs/next":
			w.WriteHeader(http.StatusNoContent)
		case "/v1/workers/goodbye":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Error("unexpected worker request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	published := make(chan struct{}, 1)
	runner, err := worker.New(worker.Config{ServerURL: server.URL, Token: "inert-desktop-token", Models: []string{"fixture:model"}, Version: "0.1.0", Logger: log.New(io.Discard, "", 0), Ollama: backend, Lister: backend, Recovery: backend, ModelCheck: time.Millisecond, OnHello: func() {}, OnStatus: desktopStatusObserver(nil, func() {
		ready.Add(1)
		select {
		case published <- struct{}{}:
		default:
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-backend.entered:
	case <-ctx.Done():
		t.Fatal("model recovery not reached")
	}
	if checks.Load() == 0 || hellos.Load() != 0 || ready.Load() != 0 {
		t.Fatal("authorization without models falsely published desktop readiness")
	}
	close(backend.release)
	select {
	case <-published:
	case <-ctx.Done():
		t.Fatal("model-ready hello did not publish readiness")
	}
	if hellos.Load() != 1 || !backend.available.Load() {
		t.Fatal("desktop readiness preceded actual hello/model readiness")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}
