package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

const (
	workerToken = "test-worker-token"
	callerToken = "test-caller-token"
	model       = "llama3.1:8b"
)

type env struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	tokens *auth.StaticTokens
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tokens := auth.NewStaticTokens()
	tokens.Add("w1", auth.RoleWorker, workerToken)
	tokens.Add("app", auth.RoleCaller, callerToken)
	s, err := New(Config{Auth: tokens, MinWorkerVersion: "1.2.0", MaxWait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return &env{t: t, srv: s, ts: ts, tokens: tokens}
}

// do sends a request; version "" omits the worker version header.
func (e *env) do(method, path, token, version string, body any) (*http.Response, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if version != "" {
		req.Header.Set(protocol.WorkerVersionHeader, version)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (e *env) hello() {
	e.t.Helper()
	res, b := e.do("POST", "/v1/workers/hello", workerToken, "1.2.0", protocol.HelloRequest{
		WorkerVersion: "1.2.0", ProtocolVersion: protocol.Version, Models: []string{model}, Capacity: 1,
	})
	if res.StatusCode != 200 {
		e.t.Fatalf("hello: %d %s", res.StatusCode, b)
	}
}

func submitReq(deadline int) protocol.SubmitRequest {
	return protocol.SubmitRequest{Model: model, Prompt: "hi", Format: "json", DeadlineMS: deadline}
}

func TestAuthRejects(t *testing.T) {
	e := newEnv(t)
	e.tokens.Add("w2", auth.RoleWorker, "revoked-token")
	e.tokens.Revoke("w2")
	cases := []struct{ name, method, path, token string }{
		{"worker missing", "GET", "/v1/jobs/next?wait=0s", ""},
		{"worker wrong", "GET", "/v1/jobs/next?wait=0s", "nope"},
		{"worker revoked", "GET", "/v1/jobs/next?wait=0s", "revoked-token"},
		{"worker uses caller token", "GET", "/v1/jobs/next?wait=0s", callerToken},
		{"hello missing", "POST", "/v1/workers/hello", ""},
		{"result wrong", "POST", "/v1/jobs/x/result", "nope"},
		{"caller missing", "GET", "/v1/status", ""},
		{"caller wrong", "POST", "/v1/jobs", "nope"},
		{"caller revoked", "GET", "/v1/jobs/x", "revoked-token"},
		{"caller uses worker token", "POST", "/v1/jobs", workerToken},
	}
	for _, c := range cases {
		res, _ := e.do(c.method, c.path, c.token, "1.2.0", nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", c.name, res.StatusCode)
		}
	}
}

func TestMinWorkerVersion(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/v1/workers/hello", "/v1/jobs/x/result"} {
		res, b := e.do("POST", path, workerToken, "1.1.9", protocol.HelloRequest{})
		var ue protocol.UpgradeError
		json.Unmarshal(b, &ue)
		if res.StatusCode != http.StatusUpgradeRequired || ue.MinWorkerVersion != "1.2.0" || ue.Error == "" {
			t.Errorf("%s: got %d %s, want 426 with min 1.2.0", path, res.StatusCode, b)
		}
	}
	res, _ := e.do("GET", "/v1/jobs/next?wait=0s", workerToken, "1.1.9", nil)
	if res.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("claim: got %d, want 426", res.StatusCode)
	}
	res, _ = e.do("GET", "/v1/jobs/next?wait=0s", workerToken, "", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("missing version: got %d, want 400", res.StatusCode)
	}
}

func TestClaimRequiresHello(t *testing.T) {
	e := newEnv(t)
	res, _ := e.do("GET", "/v1/jobs/next?wait=0s", workerToken, "1.2.0", nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("got %d, want 409", res.StatusCode)
	}
}

func TestNoWorkerOnline(t *testing.T) {
	e := newEnv(t)
	res, b := e.do("GET", "/v1/status", callerToken, "", nil)
	var st protocol.Status
	json.Unmarshal(b, &st)
	if res.StatusCode != 200 || st.WorkersOnline != 0 {
		t.Fatalf("status: %d %s", res.StatusCode, b)
	}
	start := time.Now()
	res, _ = e.do("POST", "/v1/jobs?wait=1500ms", callerToken, "", submitReq(5000))
	if res.StatusCode != http.StatusServiceUnavailable || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("submit: got %d after %v, want an immediate 503", res.StatusCode, time.Since(start))
	}
	e.hello()
	res, b = e.do("GET", "/v1/status", callerToken, "", nil)
	json.Unmarshal(b, &st)
	if st.WorkersOnline != 1 || len(st.Models) != 1 || st.Models[0] != model {
		t.Fatalf("after hello: %s", b)
	}
}

func TestRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.hello()
	done := make(chan struct{})
	go func() { // worker: claim, then answer
		defer close(done)
		res, b := e.do("GET", "/v1/jobs/next?wait=2s", workerToken, "1.2.0", nil)
		if res.StatusCode != 200 {
			t.Errorf("claim: %d %s", res.StatusCode, b)
			return
		}
		var a protocol.Assignment
		json.Unmarshal(b, &a)
		if a.Format != "json" || a.Prompt != "hi" {
			t.Errorf("assignment: %+v", a)
		}
		res, b = e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0",
			protocol.ResultRequest{LeaseID: a.LeaseID, Output: `{"ok":true}`})
		if res.StatusCode != http.StatusNoContent {
			t.Errorf("result: %d %s", res.StatusCode, b)
		}
		res, _ = e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0",
			protocol.ResultRequest{LeaseID: a.LeaseID, Output: "again"})
		if res.StatusCode != http.StatusConflict {
			t.Errorf("second result: got %d, want 409", res.StatusCode)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	res, b := e.do("POST", "/v1/jobs?wait=1500ms", callerToken, "", submitReq(5000))
	var st protocol.JobStatus
	json.Unmarshal(b, &st)
	if res.StatusCode != 200 || st.State != protocol.StateDone || st.Output != `{"ok":true}` {
		t.Fatalf("submit: %d %s", res.StatusCode, b)
	}
	<-done
	res, _ = e.do("GET", "/v1/jobs/"+st.JobID, callerToken, "", nil)
	if res.StatusCode != 200 {
		t.Errorf("get finished job: %d", res.StatusCode)
	}
	res, _ = e.do("GET", "/v1/jobs/unknown", callerToken, "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown job: got %d, want 404", res.StatusCode)
	}
}

func TestSubmitReturns202ThenExpires(t *testing.T) {
	e := newEnv(t)
	e.hello()
	res, b := e.do("POST", "/v1/jobs?wait=10ms", callerToken, "", submitReq(60))
	var st protocol.JobStatus
	json.Unmarshal(b, &st)
	if res.StatusCode != http.StatusAccepted || st.State != protocol.StateQueued {
		t.Fatalf("submit: %d %s", res.StatusCode, b)
	}
	res, b = e.do("GET", "/v1/jobs/"+st.JobID+"?wait=1s", callerToken, "", nil)
	if res.StatusCode != http.StatusGone || !strings.Contains(string(b), protocol.StateExpired) {
		t.Fatalf("after deadline: %d %s", res.StatusCode, b)
	}
	res, _ = e.do("GET", "/v1/jobs/next?wait=50ms", workerToken, "1.2.0", nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expired job dispatched: %d", res.StatusCode)
	}
}

func TestSubmitValidation(t *testing.T) {
	e := newEnv(t)
	e.hello()
	bad := []protocol.SubmitRequest{
		{Prompt: "x", DeadlineMS: 100},
		{Model: model, DeadlineMS: 100},
		{Model: model, Prompt: "x"},
		{Model: model, Prompt: "x", DeadlineMS: protocol.MaxDeadlineMS + 1},
		{Model: model, Prompt: "x", DeadlineMS: 100, Format: "yaml"},
	}
	for _, r := range bad {
		if res, _ := e.do("POST", "/v1/jobs", callerToken, "", r); res.StatusCode != 400 {
			t.Errorf("%+v: got %d, want 400", r, res.StatusCode)
		}
	}
	if res, _ := e.do("POST", "/v1/jobs?wait=soon", callerToken, "", submitReq(100)); res.StatusCode != 400 {
		t.Errorf("bad wait: got %d", res.StatusCode)
	}
}

func TestBodyLimit(t *testing.T) {
	e := newEnv(t)
	e.hello()
	big := strings.Repeat("a", MaxBodyBytes+1)
	res, _ := e.do("POST", "/v1/jobs", callerToken, "", protocol.SubmitRequest{Model: model, Prompt: big, DeadlineMS: 100})
	if res.StatusCode != 400 {
		t.Fatalf("oversized body: got %d, want 400", res.StatusCode)
	}
}

func TestLongPollAddedLatency(t *testing.T) {
	e := newEnv(t)
	e.hello()
	got := make(chan time.Time, 1)
	go func() {
		res, _ := e.do("GET", "/v1/jobs/next?wait=2s", workerToken, "1.2.0", nil)
		if res.StatusCode != 200 {
			t.Errorf("claim: %d", res.StatusCode)
		}
		got <- time.Now()
	}()
	// Wait until the worker's poll is parked on the server.
	for i := 0; ; i++ {
		e.srv.mu.Lock()
		parked := e.srv.workers["w1"].polling > 0
		e.srv.mu.Unlock()
		if parked {
			break
		}
		if i > 200 {
			t.Fatal("worker poll never parked")
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	e.do("POST", "/v1/jobs", callerToken, "", submitReq(5000))
	select {
	case at := <-got:
		d := at.Sub(start)
		t.Logf("submit → worker received job: %v", d)
		if d >= 50*time.Millisecond {
			t.Fatalf("added latency %v, want < 50ms", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll did not return when a job was queued")
	}
}
