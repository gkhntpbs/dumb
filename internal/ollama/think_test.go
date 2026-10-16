package ollama

import (
	"context"
	"encoding/json"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThinkWirePreservesFalseTrueAndOmission(t *testing.T) {
	for _, raw := range []string{"null", "false", "true"} {
		t.Run(raw, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				value, present := body["think"]
				if raw == "null" {
					if present {
						t.Error("omitted think was sent")
					}
				} else if !present || value != (raw == "true") {
					t.Errorf("think lost: %#v", body)
				}
				w.Write([]byte(`{"response":"answer"}`))
			}))
			defer ts.Close()
			c, _ := New(ts.URL)
			var req GenerateRequest
			json.Unmarshal([]byte(`{"Model":"m","Think":`+raw+`}`), &req)
			if _, err := c.Generate(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestThinkResultMetadataAndAggregateBounds(t *testing.T) {
	for _, size := range []int{protocol.MaxResultBytes - 4, protocol.MaxResultBytes - 3} {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]string{"response": "a", "thinking": strings.Repeat("x", size), "done_reason": "end"})
		}))
		client, _ := New(backend.URL)
		result, err := client.GenerateResult(context.Background(), GenerateRequest{Model: "fixture"})
		backend.Close()
		if size == protocol.MaxResultBytes-4 {
			if err != nil || result.DoneReason != "end" || len(result.Thinking) != size {
				t.Fatalf("boundary %+v %v", result, err)
			}
		} else if err == nil {
			t.Fatal("accepted oversized aggregate")
		}
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"error": strings.Repeat("x", protocol.MaxResultBytes)})
	}))
	defer backend.Close()
	client, _ := New(backend.URL)
	if _, err := client.GenerateResult(context.Background(), GenerateRequest{}); err == nil || len(err.Error()) > 4096 {
		t.Fatal("unbounded error")
	}
}

func TestThinkRejectsMalformedAndOversizedBackendDocuments(t *testing.T) {
	for _, body := range []string{
		`{"response":"unfinished"`,
		`{"response":"answer"}{"response":"extra"}`,
		`{"response":"answer","ignored":"` + strings.Repeat("x", 6*protocol.MaxResultBytes+(64<<10)) + `"}`,
	} {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		client, err := New(backend.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GenerateResult(context.Background(), GenerateRequest{Model: "fixture"})
		backend.Close()
		if err == nil {
			t.Fatal("accepted invalid backend document")
		}
	}
}
