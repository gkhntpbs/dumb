package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/server"
)

type unavailableOllama struct{ failed atomic.Bool }

func (f *unavailableOllama) Generate(ctx context.Context, r ollama.GenerateRequest) (string, error) {
	return "ok", nil
}
func (f *unavailableOllama) List(ctx context.Context) ([]string, error) {
	if f.failed.Load() {
		return nil, errors.New("fixture API unavailable")
	}
	return []string{"fixture"}, nil
}

func TestRecoveryWithdrawsUnavailableModels(t *testing.T) {
	b := make([]byte, 24)
	rand.Read(b)
	token := hex.EncodeToString(b)
	tokens := auth.NewStaticTokens()
	tokens.Add(hex.EncodeToString(b[:8]), auth.RoleWorker, token)
	service, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewServer(service)
	defer fixture.Close()
	generator := &unavailableOllama{}
	w, err := New(Config{ServerURL: fixture.URL, Token: token, Models: []string{"fixture"}, Version: "0.1.0", Ollama: generator, ModelCheck: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() { cancel(); <-done }()
	eventually(t, "fixture online", func() bool { return service.Status().WorkersOnline == 1 })
	generator.failed.Store(true)
	end := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(end) {
		if service.Status().WorkersOnline == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("worker kept unavailable models advertised")
}
