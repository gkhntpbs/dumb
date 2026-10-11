package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

func TestGenerationStopsAfterLeaseLoss(t *testing.T) {
	var acks, results atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/jobs/job/ack" {
			if acks.Add(1) == 1 {
				w.WriteHeader(204)
			} else {
				w.WriteHeader(409)
			}
			return
		}
		results.Add(1)
		w.WriteHeader(204)
	}))
	defer ts.Close()
	ended := make(chan struct{})
	gen := genFunc(func(ctx context.Context, r ollama.GenerateRequest) (string, error) {
		<-ctx.Done()
		close(ended)
		return "", ctx.Err()
	})
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "0.1.0", Ollama: gen})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		w.runJob(ctx, protocol.Assignment{JobID: "job", LeaseID: "lease", LeaseExpiresAt: time.Now().Add(5 * time.Second)})
		close(done)
	}()
	select {
	case <-ended:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("generation continued after lease loss")
	}
	<-done
	if results.Load() != 0 {
		t.Fatalf("posted %d stale results", results.Load())
	}
}

func TestGenerationUsesAssignmentLimitAndDeadline(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer ts.Close()
	seen := make(chan map[string]any, 1)
	gen := genFunc(func(ctx context.Context, r ollama.GenerateRequest) (string, error) {
		b, _ := json.Marshal(r)
		var fields map[string]any
		json.Unmarshal(b, &fields)
		seen <- fields
		<-ctx.Done()
		return "", ctx.Err()
	})
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "0.1.0", Ollama: gen})
	if err != nil {
		t.Fatal(err)
	}
	var a protocol.Assignment
	json.Unmarshal([]byte(`{"job_id":"job","lease_id":"lease","num_predict":17}`), &a)
	a.LeaseExpiresAt = time.Now().Add(80 * time.Millisecond)
	begin := time.Now()
	w.runJob(context.Background(), a)
	if time.Since(begin) > time.Second {
		t.Fatal("generation exceeded deadline")
	}
	if fields := <-seen; fields["NumPredict"] != float64(17) {
		t.Fatalf("generation limit: %v", fields)
	}
}
