package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// TestCallerCancelStopsGeneration: a caller cancelling a leased job reaches
// the worker through its lease check (409 on ack); the generation stops, no
// result is posted, and the freed slot takes the next job.
func TestCallerCancelStopsGeneration(t *testing.T) {
	var results atomic.Int32
	ts, s := ackServer(t, time.Second, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/result") {
				results.Add(1)
			}
			h.ServeHTTP(w, r)
		})
	})
	started := make(chan string, 2)
	ended := make(chan error, 2)
	var calls atomic.Int32
	gen := genFunc(func(ctx context.Context, r ollama.GenerateRequest) (string, error) {
		if calls.Add(1) > 1 {
			return `{"ok":true}`, nil
		}
		started <- r.Prompt
		<-ctx.Done()
		ended <- ctx.Err()
		return "", ctx.Err()
	})
	w, err := New(Config{ServerURL: ts.URL, Token: workerToken, Models: []string{model}, Version: "0.1.0", Ollama: gen})
	if err != nil {
		t.Fatal(err)
	}
	stop := start(t, w)
	defer stop()
	eventually(t, "online", func() bool { return s.Status().WorkersOnline == 1 })
	code, body := caller(t, "POST", ts.URL+"/v1/jobs", protocol.SubmitRequest{Model: model, Prompt: "first", DeadlineMS: 10000})
	if code != http.StatusAccepted {
		t.Fatalf("submit: %d %s", code, body)
	}
	var job protocol.JobStatus
	json.Unmarshal(body, &job)
	<-started
	// Let the first ack succeed so the periodic lease check delivers the cancel.
	time.Sleep(300 * time.Millisecond)
	cancelled := time.Now()
	if code, body := caller(t, "POST", ts.URL+"/v1/jobs/"+job.JobID+"/cancel", nil); code != http.StatusOK {
		t.Fatalf("cancel: %d %s", code, body)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("generation kept running after cancel")
	}
	t.Logf("generation stopped %s after cancel", time.Since(cancelled).Round(time.Millisecond))
	if code, st := submit(t, ts.URL, model); code != http.StatusOK || st.State != protocol.StateDone {
		t.Fatalf("next job after cancel: %d %+v", code, st)
	}
	if results.Load() != 1 {
		t.Fatalf("results posted %d, want only the next job's", results.Load())
	}
}
