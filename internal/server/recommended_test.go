package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

func TestRecommendedWorkerVersionAdvertised(t *testing.T) {
	e := newEnvWith(t, func(c *Config) { c.RecommendedWorkerVersion = "v1.4.0" })
	res, b := e.do("POST", "/v1/workers/hello", workerToken, "1.2.0", protocol.HelloRequest{
		WorkerVersion: "1.2.0", ProtocolVersion: protocol.Version, Models: []string{model}, Capacity: 1,
	})
	var hello protocol.HelloResponse
	json.Unmarshal(b, &hello)
	if res.StatusCode != http.StatusOK || hello.RecommendedWorkerVersion != "1.4.0" || hello.MinWorkerVersion != "1.2.0" {
		t.Fatalf("hello: %d %s, want recommended 1.4.0 next to min 1.2.0", res.StatusCode, b)
	}
	res, b = e.do("GET", "/v1/workers/me", workerToken, "1.2.0", nil)
	var me protocol.WhoAmI
	json.Unmarshal(b, &me)
	if res.StatusCode != http.StatusOK || me.RecommendedWorkerVersion != "1.4.0" {
		t.Fatalf("whoami: %d %s, want recommended 1.4.0", res.StatusCode, b)
	}
	// Being older than the recommendation is not an error; the floor still is.
	res, b = e.do("GET", "/v1/workers/me", workerToken, "1.1.0", nil)
	var ue protocol.UpgradeError
	json.Unmarshal(b, &ue)
	if res.StatusCode != http.StatusUpgradeRequired || ue.MinWorkerVersion != "1.2.0" {
		t.Fatalf("below floor: %d %s, want the unchanged 426", res.StatusCode, b)
	}
}

func TestRecommendedWorkerVersionOmittedByDefault(t *testing.T) {
	e := newEnv(t)
	res, b := e.do("POST", "/v1/workers/hello", workerToken, "1.2.0", protocol.HelloRequest{
		WorkerVersion: "1.2.0", ProtocolVersion: protocol.Version, Models: []string{model}, Capacity: 1,
	})
	if res.StatusCode != http.StatusOK || strings.Contains(string(b), "recommended_worker_version") {
		t.Fatalf("hello: %d %s, want no recommended_worker_version", res.StatusCode, b)
	}
	_, b = e.do("GET", "/v1/workers/me", workerToken, "1.2.0", nil)
	if strings.Contains(string(b), "recommended_worker_version") {
		t.Fatalf("whoami: %s, want no recommended_worker_version", b)
	}
}

func TestRecommendedWorkerVersionValidated(t *testing.T) {
	tokens := auth.NewStaticTokens()
	for _, v := range []string{"nope", "1.2", "1.1.9"} {
		_, err := New(Config{Auth: tokens, MinWorkerVersion: "1.2.0", RecommendedWorkerVersion: v})
		if err == nil {
			t.Errorf("recommended %q with floor 1.2.0: accepted", v)
		}
	}
	if _, err := New(Config{Auth: tokens, MinWorkerVersion: "1.2.0", RecommendedWorkerVersion: "1.2.0"}); err != nil {
		t.Errorf("recommended equal to the floor: %v", err)
	}
}
