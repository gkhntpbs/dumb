package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGenerateAlwaysBoundsTokens(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		options, ok := body["options"].(map[string]any)
		if !ok || options["num_predict"] != float64(128) {
			t.Errorf("unbounded request: %v", body)
		}
		json.NewEncoder(w).Encode(map[string]string{"response": "ok"})
	}))
	defer ts.Close()
	c, _ := New(ts.URL)
	if _, err := c.Generate(context.Background(), GenerateRequest{Model: "m", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateDeadlineClosesRequest(t *testing.T) {
	canceled := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer ts.Close()
	c, _ := New(ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Generate(ctx, GenerateRequest{Model: "m", Prompt: "p"}); err == nil {
		t.Fatal("deadline ignored")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Ollama request remained open")
	}
}

func TestGenerateCustomTokenLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Options struct {
				NumPredict int `json:"num_predict"`
			} `json:"options"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Options.NumPredict != 17 {
			t.Errorf("limit = %d", body.Options.NumPredict)
		}
		json.NewEncoder(w).Encode(map[string]string{"response": "ok"})
	}))
	defer ts.Close()
	c, _ := New(ts.URL)
	var request GenerateRequest
	json.Unmarshal([]byte(`{"Model":"m","Prompt":"p","NumPredict":17}`), &request)
	if _, err := c.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for _, value := range []int{-2, -1, 129} {
		json.Unmarshal([]byte(fmt.Sprintf(`{"Model":"m","Prompt":"p","NumPredict":%d}`, value)), &request)
		if _, err := c.Generate(context.Background(), request); err == nil {
			t.Fatalf("accepted limit %d", value)
		}
	}
}
