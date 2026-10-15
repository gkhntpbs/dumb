package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

func (e *env) submitJob(deadline int) string {
	e.t.Helper()
	res, body := e.do("POST", "/v1/jobs", callerToken, "", submitReq(deadline))
	if res.StatusCode != 202 {
		e.t.Fatalf("submit: %d %s", res.StatusCode, body)
	}
	var st protocol.JobStatus
	if err := json.Unmarshal(body, &st); err != nil {
		e.t.Fatal(err)
	}
	return st.JobID
}

func (e *env) cancel(id, token string) (int, protocol.JobStatus, string) {
	e.t.Helper()
	res, body := e.do("POST", "/v1/jobs/"+id+"/cancel", token, "", nil)
	var st protocol.JobStatus
	json.Unmarshal(body, &st)
	return res.StatusCode, st, string(body)
}

func TestCancelQueuedHTTP(t *testing.T) {
	e := newEnv(t)
	e.hello()
	id := e.submitJob(10000)
	code, st, body := e.cancel(id, callerToken)
	if code != 200 || st.State != protocol.StateCancelled || st.JobID != id {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if code, _ := e.claim("100ms"); code != 204 {
		t.Fatalf("cancelled job dispatched: %d", code)
	}
	res, b := e.do("GET", "/v1/jobs/"+id+"?wait=1s", callerToken, "", nil)
	if res.StatusCode != 410 || !json.Valid(b) {
		t.Fatalf("get cancelled: %d %s", res.StatusCode, b)
	}
	json.Unmarshal(b, &st)
	if st.State != protocol.StateCancelled {
		t.Fatalf("get: %s", b)
	}
	// Repeat cancel is idempotent.
	for i := 0; i < 2; i++ {
		if code, st, body := e.cancel(id, callerToken); code != 200 || st.State != protocol.StateCancelled {
			t.Fatalf("repeat cancel: %d %s", code, body)
		}
	}
}

func TestCancelLeasedHTTPRefusesAckAndResult(t *testing.T) {
	e := newEnv(t)
	e.helloAs(workerToken, protocol.FeatureAck)
	id := e.submitJob(10000)
	code, a := e.claim("1s")
	if code != 200 || a.JobID != id {
		t.Fatalf("claim: %d", code)
	}
	waiting := make(chan protocol.JobStatus, 1)
	go func() {
		_, b := e.do("GET", "/v1/jobs/"+id+"?wait=5s", callerToken, "", nil)
		var st protocol.JobStatus
		json.Unmarshal(b, &st)
		waiting <- st
	}()
	time.Sleep(30 * time.Millisecond)
	if code, st, body := e.cancel(id, callerToken); code != 200 || st.State != protocol.StateCancelled {
		t.Fatalf("cancel leased: %d %s", code, body)
	}
	select {
	case st := <-waiting:
		if st.State != protocol.StateCancelled {
			t.Fatalf("waiting caller got %+v", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting caller not woken by cancel")
	}
	res, b := e.do("POST", "/v1/jobs/"+id+"/ack", workerToken, "1.2.0", protocol.AckRequest{LeaseID: a.LeaseID})
	if res.StatusCode != 409 {
		t.Fatalf("ack after cancel: %d %s", res.StatusCode, b)
	}
	res, b = e.do("POST", "/v1/jobs/"+id+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a.LeaseID, Output: "late"})
	if res.StatusCode != 409 {
		t.Fatalf("result after cancel: %d %s", res.StatusCode, b)
	}
	res, b = e.do("GET", "/v1/jobs/"+id, callerToken, "", nil)
	var st protocol.JobStatus
	json.Unmarshal(b, &st)
	if res.StatusCode != 410 || st.State != protocol.StateCancelled || st.Output != "" {
		t.Fatalf("result not discarded: %d %s", res.StatusCode, b)
	}
}

func TestCancelAfterCompleteHTTP(t *testing.T) {
	e := newEnv(t)
	e.hello()
	id := e.submitJob(10000)
	_, a := e.claim("1s")
	res, b := e.do("POST", "/v1/jobs/"+id+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a.LeaseID, Output: "kept"})
	if res.StatusCode != 204 {
		t.Fatalf("result: %d %s", res.StatusCode, b)
	}
	code, st, body := e.cancel(id, callerToken)
	if code != 409 || st.State != protocol.StateDone || st.Output != "kept" {
		t.Fatalf("cancel after complete: %d %s", code, body)
	}
}

func TestCancelAfterDeadlineHTTP(t *testing.T) {
	e := newEnv(t)
	e.hello()
	id := e.submitJob(10000)
	e.srv.q.SetClock(func() time.Time { return time.Now().Add(15 * time.Second) })
	code, st, body := e.cancel(id, callerToken)
	if code != 409 || st.State != protocol.StateExpired {
		t.Fatalf("cancel after deadline: %d %s", code, body)
	}
}

func TestCancelOwnershipAndRoles(t *testing.T) {
	e := newEnv(t)
	e.hello()
	e.tokens.Add("other-app", auth.RoleCaller, "other-caller-token")
	e.tokens.Add("reader", auth.RoleAdminRead, "admin-read-token")
	e.tokens.Add("manager", auth.RoleAdminManage, "admin-manage-token")
	id := e.submitJob(10000)
	code, _, body := e.cancel(id, "other-caller-token")
	unknownCode, _, unknownBody := e.cancel("unknown", "other-caller-token")
	if code != 404 || unknownCode != 404 || body != unknownBody {
		t.Fatalf("other caller: %d %s vs %d %s", code, body, unknownCode, unknownBody)
	}
	for _, token := range []string{workerToken, "admin-read-token", "admin-manage-token", "", "invalid"} {
		if code, _, body := e.cancel(id, token); code != 401 {
			t.Fatalf("token %q: %d %s", token, code, body)
		}
	}
	res, b := e.do("GET", "/v1/jobs/"+id, callerToken, "", nil)
	if res.StatusCode != 202 {
		t.Fatalf("job changed by refused cancels: %d %s", res.StatusCode, b)
	}
}

func TestCancelSurvivesRestartHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	e := newEnvWith(t, func(c *Config) { c.QueueStateDirectory = dir })
	e.hello()
	id := e.submitJob(30000)
	_, a := e.claim("1s")
	if code, _, body := e.cancel(id, callerToken); code != 200 {
		t.Fatalf("cancel: %d %s", code, body)
	}
	e.ts.Close()
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{Auth: e.tokens, MinWorkerVersion: "1.2.0", QueueStateDirectory: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.srv = reopened
	e.ts = httptest.NewServer(reopened)
	defer e.ts.Close()
	e.hello()
	res, b := e.do("GET", "/v1/jobs/"+id, callerToken, "", nil)
	if res.StatusCode != 410 {
		t.Fatalf("after restart: %d %s", res.StatusCode, b)
	}
	if code, st, body := e.cancel(id, callerToken); code != 200 || st.State != protocol.StateCancelled {
		t.Fatalf("repeat after restart: %d %s", code, body)
	}
	res, b = e.do("POST", "/v1/jobs/"+id+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a.LeaseID, Output: "late"})
	if res.StatusCode != 409 {
		t.Fatalf("result after restart: %d %s", res.StatusCode, b)
	}
	if code, _ := e.claim("100ms"); code != 204 {
		t.Fatal("cancelled job dispatched after restart")
	}
}

func TestAdminCountsCancelled(t *testing.T) {
	e := newEnv(t)
	e.hello()
	id := e.submitJob(10000)
	e.cancel(id, callerToken)
	if o := e.srv.q.Observe("", protocol.StateCancelled, 0, 10); o.Cancelled != 1 || o.Total != 1 {
		t.Fatalf("observation: %+v", o)
	}
}
