package server

import (
	"encoding/json"
	"testing"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/queue"
)

func TestAdmissionHTTPCallerAndGlobalCapacity(t *testing.T) {
	e := newEnvWith(t, func(cfg *Config) {
		cfg.QueueLimits = queue.Limits{MaxActiveJobs: 2, MaxJobs: 4, MaxBytes: 4 << 20, CallerMaxActiveJobs: 1, CallerMaxJobs: 2, CallerMaxBytes: 2 << 20}
	})
	e.hello()
	e.tokens.Add("other-app", auth.RoleCaller, "other-caller-token")
	e.tokens.Add("third-app", auth.RoleCaller, "third-caller-token")
	for _, token := range []string{callerToken, "other-caller-token"} {
		res, body := e.do("POST", "/v1/jobs", token, "", submitReq(10000))
		if res.StatusCode != 202 {
			t.Fatalf("accept: %d %s", res.StatusCode, body)
		}
	}
	for _, tc := range []struct {
		token string
		code  int
	}{{callerToken, 429}, {"third-caller-token", 503}} {
		before := e.srv.q.Observe("", "", 0, 0).Total
		res, body := e.do("POST", "/v1/jobs", tc.token, "", submitReq(10000))
		if res.StatusCode != tc.code || res.Header.Get("Retry-After") != "1" {
			t.Fatalf("capacity: %d %s retry%s", res.StatusCode, body, res.Header.Get("Retry-After"))
		}
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if _, ok := result["job_id"]; ok {
			t.Fatal("rejected admission disclosed job")
		}
		if after := e.srv.q.Observe("", "", 0, 0).Total; after != before {
			t.Fatalf("rejection allocated: %d -> %d", before, after)
		}
	}
}

func TestOwnershipCannotBeSuppliedByCaller(t *testing.T) {
	e := newEnv(t)
	e.hello()
	e.tokens.Add("other-app", auth.RoleCaller, "other-caller-token")
	res, body := e.do("POST", "/v1/jobs", callerToken, "", map[string]any{"model": model, "prompt": "hi", "deadline_ms": 10000, "owner": "other-app", "caller_id": "other-app"})
	if res.StatusCode != 202 {
		t.Fatalf("submit: %d %s", res.StatusCode, body)
	}
	var status protocol.JobStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	res, body = e.do("GET", "/v1/jobs/"+status.JobID, "other-caller-token", "", nil)
	if res.StatusCode != 404 {
		t.Fatalf("supplied ownership applied: %d %s", res.StatusCode, body)
	}
	res, body = e.do("GET", "/v1/jobs/"+status.JobID, callerToken, "", nil)
	if res.StatusCode != 202 {
		t.Fatalf("auth owner lost: %d %s", res.StatusCode, body)
	}
}
