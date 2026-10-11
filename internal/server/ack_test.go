package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

const worker2Token = "test-worker2-token"

// helloAs says hello for token with the given features.
func (e *env) helloAs(token string, features ...string) {
	e.t.Helper()
	res, b := e.do("POST", "/v1/workers/hello", token, "1.2.0", protocol.HelloRequest{
		WorkerVersion: "1.2.0", ProtocolVersion: protocol.Version, Models: []string{model}, Capacity: 1, Features: features,
	})
	if res.StatusCode != 200 {
		e.t.Fatalf("hello: %d %s", res.StatusCode, b)
	}
}

func (e *env) claimAs(token, wait string) (int, protocol.Assignment) {
	e.t.Helper()
	res, b := e.do("GET", "/v1/jobs/next?wait="+wait, token, "1.2.0", nil)
	var a protocol.Assignment
	json.Unmarshal(b, &a)
	return res.StatusCode, a
}

func ackEnv(t *testing.T) *env {
	e := newEnvWith(t, func(c *Config) { c.AckWindow = 100 * time.Millisecond })
	e.tokens.Add("w2", auth.RoleWorker, worker2Token)
	return e
}

// TestUnackedLeaseRevoked: an ack-capable worker that never acks loses the
// lease after the ack window; the job goes to another worker and the old
// lease's late ack and result get 409.
func TestUnackedLeaseRevoked(t *testing.T) {
	e := ackEnv(t)
	e.helloAs(workerToken, protocol.FeatureAck)
	e.helloAs(worker2Token, protocol.FeatureAck)
	_, b := e.do("POST", "/v1/jobs", callerToken, "", submitReq(5000))
	var job protocol.JobStatus
	json.Unmarshal(b, &job)
	c, a := e.claimAs(workerToken, "0s")
	if c != 200 || a.JobID != job.JobID {
		t.Fatalf("claim: %d %+v", c, a)
	}
	start := time.Now()
	c, a2 := e.claimAs(worker2Token, "2s") // gets the revoked job
	if c != 200 || a2.JobID != job.JobID || a2.LeaseID == a.LeaseID {
		t.Fatalf("second worker: %d %+v", c, a2)
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > time.Second {
		t.Errorf("revoked after %v, want about the 100ms window", d)
	}
	if res, _ := e.do("POST", "/v1/jobs/"+a.JobID+"/ack", workerToken, "1.2.0", protocol.AckRequest{LeaseID: a.LeaseID}); res.StatusCode != http.StatusConflict {
		t.Errorf("late ack: %d, want 409", res.StatusCode)
	}
	if res, _ := e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a.LeaseID, Output: "x"}); res.StatusCode != http.StatusConflict {
		t.Errorf("late result: %d, want 409", res.StatusCode)
	}
	if res, _ := e.do("POST", "/v1/jobs/"+a2.JobID+"/ack", worker2Token, "1.2.0", protocol.AckRequest{LeaseID: a2.LeaseID}); res.StatusCode != 204 {
		t.Fatalf("ack: %d", res.StatusCode)
	}
	time.Sleep(200 * time.Millisecond) // past the window: an acked lease stays
	if res, _ := e.do("POST", "/v1/jobs/"+a2.JobID+"/result", worker2Token, "1.2.0", protocol.ResultRequest{LeaseID: a2.LeaseID, Output: "ok"}); res.StatusCode != 204 {
		t.Fatalf("result after ack: %d", res.StatusCode)
	}
	if res, b := e.do("GET", "/v1/jobs/"+job.JobID, callerToken, "", nil); res.StatusCode != 200 || !json.Valid(b) {
		t.Fatalf("job: %d %s", res.StatusCode, b)
	}
}

// TestWorkerWithoutAckFeatureKeepsLease: an older worker (no "ack" in hello)
// is never subject to the ack window.
func TestWorkerWithoutAckFeatureKeepsLease(t *testing.T) {
	e := ackEnv(t)
	e.helloAs(workerToken)
	e.do("POST", "/v1/jobs", callerToken, "", submitReq(5000))
	c, a := e.claimAs(workerToken, "0s")
	if c != 200 {
		t.Fatalf("claim: %d", c)
	}
	time.Sleep(250 * time.Millisecond)
	if e.srv.q.Pending() != 0 {
		t.Fatal("lease of a worker without ack was revoked")
	}
	if res, _ := e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a.LeaseID, Output: "x"}); res.StatusCode != 204 {
		t.Fatalf("result: %d", res.StatusCode)
	}
}

// TestAckWindowNeverBeyondDeadline: a job whose deadline is shorter than the
// window expires at its deadline, not later, and is not requeued.
func TestAckWindowNeverBeyondDeadline(t *testing.T) {
	e := newEnvWith(t, func(c *Config) { c.AckWindow = time.Hour })
	e.helloAs(workerToken, protocol.FeatureAck)
	_, b := e.do("POST", "/v1/jobs", callerToken, "", submitReq(150))
	var job protocol.JobStatus
	json.Unmarshal(b, &job)
	if c, _ := e.claimAs(workerToken, "0s"); c != 200 {
		t.Fatalf("claim: %d", c)
	}
	res, _ := e.do("GET", "/v1/jobs/"+job.JobID+"?wait=1s", callerToken, "", nil)
	if res.StatusCode != http.StatusGone || e.srv.q.Pending() != 0 {
		t.Fatalf("got %d pending %d, want 410 and nothing requeued", res.StatusCode, e.srv.q.Pending())
	}
}

// TestGoodbye: after goodbye the worker is offline at once (503, not a wait),
// its parked poll returns, and hello brings it back.
func TestGoodbye(t *testing.T) {
	e := newEnv(t)
	e.hello()
	polled := make(chan int, 1)
	go func() { c, _ := e.claimAs(workerToken, "2s"); polled <- c }()
	for i := 0; ; i++ {
		e.srv.mu.Lock()
		parked := e.srv.workers["w1"].polling > 0
		e.srv.mu.Unlock()
		if parked {
			break
		}
		if i > 200 {
			t.Fatal("poll never parked")
		}
		time.Sleep(time.Millisecond)
	}
	if res, _ := e.do("POST", "/v1/workers/goodbye", workerToken, "1.2.0", nil); res.StatusCode != 204 {
		t.Fatalf("goodbye: %d", res.StatusCode)
	}
	select {
	case c := <-polled:
		if c != http.StatusNoContent {
			t.Errorf("parked poll: %d, want 204", c)
		}
	case <-time.After(time.Second):
		t.Fatal("goodbye did not end the parked poll")
	}
	if st := e.srv.Status(); st.WorkersOnline != 0 || len(st.Models) != 0 {
		t.Fatalf("status after goodbye: %+v", st)
	}
	start := time.Now()
	if res, _ := e.do("POST", "/v1/jobs?wait=1s", callerToken, "", submitReq(5000)); res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("submit after goodbye: %d, want 503", res.StatusCode)
	} else if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("503 took %v", d)
	}
	if res, _ := e.do("POST", "/v1/workers/goodbye", workerToken, "1.2.0", nil); res.StatusCode != 204 {
		t.Fatalf("second goodbye: %d", res.StatusCode)
	}
	if c, _ := e.claimAs(workerToken, "0s"); c != http.StatusConflict {
		t.Fatalf("claim after goodbye: %d, want 409 (hello first)", c)
	}
	e.hello()
	if st := e.srv.Status(); st.WorkersOnline != 1 {
		t.Fatalf("status after hello: %+v", st)
	}
}

// TestWorkerWithoutModelsNotOnline: a worker record with no models is not
// counted, whatever its last-seen time.
func TestWorkerWithoutModelsNotOnline(t *testing.T) {
	e := newEnv(t)
	e.hello()
	e.srv.mu.Lock()
	e.srv.workers["w1"].models = nil
	e.srv.mu.Unlock()
	if st := e.srv.Status(); st.WorkersOnline != 0 {
		t.Fatalf("worker without models counted: %+v", st)
	}
}

// TestAckAddsNoLatency: submit → claim → ack → result → caller has the
// answer, all well under 50ms on loopback.
func TestAckAddsNoLatency(t *testing.T) {
	e := newEnv(t)
	e.helloAs(workerToken, protocol.FeatureAck)
	go func() {
		c, a := e.claimAs(workerToken, "2s")
		if c != 200 {
			return
		}
		e.do("POST", "/v1/jobs/"+a.JobID+"/ack", workerToken, "1.2.0", protocol.AckRequest{LeaseID: a.LeaseID})
		e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a.LeaseID, Output: "{}"})
	}()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	res, _ := e.do("POST", "/v1/jobs?wait=1s", callerToken, "", submitReq(5000))
	d := time.Since(start)
	t.Logf("submit → result with ack: %v", d)
	if res.StatusCode != 200 || d >= 50*time.Millisecond {
		t.Fatalf("got %d in %v, want 200 in < 50ms", res.StatusCode, d)
	}
}
