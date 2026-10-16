package server

import (
	"encoding/json"
	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"strings"
	"testing"
)

func TestThinkRequiresCapableOnlineWorker(t *testing.T) {
	e := newEnv(t)
	e.hello()
	res, body := e.do("POST", "/v1/jobs", callerToken, "", map[string]any{"model": model, "prompt": "p", "deadline_ms": 5000, "think": false})
	if res.StatusCode != 503 {
		t.Fatalf("explicit think routed to legacy worker: %d %s", res.StatusCode, body)
	}
}

func TestThinkRejectsUnsupportedValueTypes(t *testing.T) {
	for _, value := range []any{nil, "low", 17, map[string]any{}} {
		e := newEnv(t)
		e.hello()
		res, body := e.do("POST", "/v1/jobs", callerToken, "", map[string]any{"model": model, "prompt": "p", "deadline_ms": 5000, "think": value})
		if res.StatusCode != 400 {
			t.Fatalf("accepted %v: %d %s", value, res.StatusCode, body)
		}
	}
}

func TestThinkMixedWorkersNeverDowngradeAssignment(t *testing.T) {
	e := newEnv(t)
	e.hello()
	const capableToken = "inert-think-worker"
	e.tokens.Add("think-worker", auth.RoleWorker, capableToken)
	res, b := e.do("POST", "/v1/workers/hello", capableToken, "1.2.0", protocol.HelloRequest{WorkerVersion: "1.2.0", ProtocolVersion: 1, Models: []string{model}, Capacity: 1, Features: []string{protocol.FeatureThink}})
	if res.StatusCode != 200 {
		t.Fatalf("hello%d%s", res.StatusCode, b)
	}
	req := map[string]any{"model": model, "prompt": "p", "deadline_ms": 5000, "think": false}
	res, b = e.do("POST", "/v1/jobs", callerToken, "", req)
	if res.StatusCode != 202 {
		t.Fatalf("submit%d%s", res.StatusCode, b)
	}
	res, b = e.do("GET", "/v1/jobs/next?wait=0s", workerToken, "1.2.0", nil)
	if res.StatusCode != 204 {
		t.Fatalf("legacy claimed%d%s", res.StatusCode, b)
	}
	res, b = e.do("GET", "/v1/jobs/next?wait=1s", capableToken, "1.2.0", nil)
	if res.StatusCode != 200 {
		t.Fatalf("capable missed%d%s", res.StatusCode, b)
	}
	var assignment protocol.Assignment
	if err := json.Unmarshal(b, &assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.Think == nil || *assignment.Think {
		t.Fatalf("false lost%s", b)
	}
}

func TestThinkResultHTTPEnvelopeAndCombinedLimit(t *testing.T) {
	e := newEnv(t)
	e.hello()
	res, b := e.do("POST", "/v1/jobs", callerToken, "", map[string]any{"model": model, "prompt": "p", "deadline_ms": 5000})
	if res.StatusCode != 202 {
		t.Fatal(string(b))
	}
	res, b = e.do("GET", "/v1/jobs/next?wait=1s", workerToken, "1.2.0", nil)
	var a protocol.Assignment
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatal(err)
	}
	result := protocol.ResultRequest{LeaseID: a.LeaseID, Thinking: strings.Repeat("\x01", protocol.MaxResultBytes-1), DoneReason: "xy"}
	res, b = e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0", result)
	if res.StatusCode != 400 {
		t.Fatalf("oversized result %d%s", res.StatusCode, b)
	}
	result.DoneReason = "x"
	res, b = e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0", result)
	if res.StatusCode != 204 {
		t.Fatalf("valid escaped boundary %d%s", res.StatusCode, b)
	}
	res, b = e.do("GET", "/v1/jobs/"+a.JobID, callerToken, "", nil)
	var status protocol.JobStatus
	if err := json.Unmarshal(b, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != protocol.StateDone || len(status.Thinking) != protocol.MaxResultBytes-1 || status.DoneReason != "x" {
		t.Fatal("metadata boundary lost")
	}
}
