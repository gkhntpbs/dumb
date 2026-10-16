package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

func TestThinkEndToEndWithRealOllamaHTTPClient(t *testing.T) {
	for _, mode := range []string{"omitted", "false", "true"} {
		t.Run(mode, func(t *testing.T) {
			seen := make(chan map[string]any, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					json.NewEncoder(rw).Encode(map[string]any{"models": []map[string]string{{"name": model}}})
					return
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				seen <- request
				result := map[string]string{"response": "fixture-answer", "done_reason": "stop"}
				if mode == "true" {
					result["response"] = ""
					result["thinking"] = "fixture-thought"
					result["done_reason"] = "length"
				}
				json.NewEncoder(rw).Encode(result)
			}))
			defer backend.Close()
			client, _ := ollama.New(backend.URL)
			srv := newServer(t, "0.1.0", nil)
			w, err := New(Config{ServerURL: srv.URL, Token: workerToken, Models: []string{model}, Capacity: 1, Version: "0.3.4", Ollama: client})
			if err != nil {
				t.Fatal(err)
			}
			stop := start(t, w)
			defer stop()
			request := protocol.SubmitRequest{Model: model, Prompt: "fixture", DeadlineMS: 5000}
			if mode != "omitted" {
				value := mode == "true"
				request.Think = &value
			}
			payload, _ := json.Marshal(request)
			var status protocol.JobStatus
			for attempt := 0; attempt < 100; attempt++ {
				req, _ := http.NewRequest("POST", srv.URL+"/v1/jobs?wait=2s", bytes.NewReader(payload))
				req.Header.Set("Authorization", "Bearer "+callerToken)
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode == 503 {
					time.Sleep(5 * time.Millisecond)
					continue
				}
				if response.StatusCode != 200 {
					t.Fatalf("status %d %s", response.StatusCode, body)
				}
				if err := json.Unmarshal(body, &status); err != nil {
					t.Fatal(err)
				}
				break
			}
			if status.State != protocol.StateDone {
				t.Fatalf("job not completed %+v", status)
			}
			if mode == "true" {
				if status.Output != "" || status.Thinking != "fixture-thought" || status.DoneReason != "length" {
					t.Fatalf("metadata lost %+v", status)
				}
			} else if status.Output != "fixture-answer" || status.DoneReason != "stop" {
				t.Fatalf("answer lost %+v", status)
			}
			select {
			case body := <-seen:
				value, present := body["think"]
				if mode == "omitted" {
					if present {
						t.Fatalf("omission lost: %v", body)
					}
				} else if !present || value != (mode == "true") {
					t.Fatalf("think lost: %v", body)
				}
			case <-time.After(time.Second):
				t.Fatal("no Ollama request")
			}
		})
	}
}

func TestLegacyGeneratorDoesNotAdvertiseThink(t *testing.T) {
	w, err := New(Config{ServerURL: "http://127.0.0.1:1", Token: workerToken, Models: []string{model}, Version: "0.3.4", Ollama: genFunc(func(context.Context, ollama.GenerateRequest) (string, error) { return "ok", nil })})
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range w.features() {
		if feature == protocol.FeatureThink {
			t.Fatal("legacy generator advertises think")
		}
	}
}
