package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

func TestCallerOwnershipQueuedJob(t *testing.T) {
	e := newEnv(t)
	e.hello()
	e.tokens.Add("other-app", auth.RoleCaller, "other-caller-token")
	res, body := e.do("POST", "/v1/jobs", callerToken, "", submitReq(10000))
	if res.StatusCode != 202 {
		t.Fatalf("submit: %d %s", res.StatusCode, body)
	}
	var status protocol.JobStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	res, body = e.do("GET", "/v1/jobs/"+status.JobID+"?wait=10s", "other-caller-token", "", nil)
	if res.StatusCode != 404 {
		t.Fatalf("other caller read: %d %s", res.StatusCode, body)
	}
}

func TestCallerOwnershipEveryLifecycleState(t *testing.T) {
	for _, state := range []string{protocol.StateQueued, protocol.StateLeased, protocol.StateDone, protocol.StateFailed, protocol.StateExpired} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			e.hello()
			e.tokens.Add("other-app", auth.RoleCaller, "other-caller-token")
			res, body := e.do("POST", "/v1/jobs", callerToken, "", submitReq(10000))
			if res.StatusCode != 202 {
				t.Fatalf("submit: %d %s", res.StatusCode, body)
			}
			var status protocol.JobStatus
			if err := json.Unmarshal(body, &status); err != nil {
				t.Fatal(err)
			}
			if state != protocol.StateQueued && state != protocol.StateExpired {
				res, body = e.do("GET", "/v1/jobs/next?wait=1s", workerToken, "1.2.0", nil)
				if res.StatusCode != 200 {
					t.Fatalf("claim: %d %s", res.StatusCode, body)
				}
				var assignment protocol.Assignment
				if err := json.Unmarshal(body, &assignment); err != nil {
					t.Fatal(err)
				}
				if state == protocol.StateDone || state == protocol.StateFailed {
					result := protocol.ResultRequest{LeaseID: assignment.LeaseID}
					if state == protocol.StateDone {
						result.Output = "private output"
					} else {
						result.Error = "private error"
					}
					res, body = e.do("POST", "/v1/jobs/"+status.JobID+"/result", workerToken, "1.2.0", result)
					if res.StatusCode != 204 {
						t.Fatalf("complete: %d %s", res.StatusCode, body)
					}
				}
			}
			if state == protocol.StateExpired {
				e.srv.q.SetClock(func() time.Time { return time.Now().Add(time.Minute) })
			}
			start := time.Now()
			res, body = e.do("GET", "/v1/jobs/"+status.JobID+"?wait=1s", "other-caller-token", "", nil)
			if res.StatusCode != 404 || time.Since(start) > 500*time.Millisecond {
				t.Fatalf("other caller: %d %s took %s", res.StatusCode, body, time.Since(start))
			}
			unknown, unknownBody := e.do("GET", "/v1/jobs/unknown?wait=1s", "other-caller-token", "", nil)
			if unknown.StatusCode != res.StatusCode || string(unknownBody) != string(body) {
				t.Fatalf("existence disclosed: %s != %s", body, unknownBody)
			}
			e.tokens.Add("app", auth.RoleCaller, "rotated-caller-token")
			res, body = e.do("GET", "/v1/jobs/"+status.JobID, "rotated-caller-token", "", nil)
			want := 202
			if state == protocol.StateDone || state == protocol.StateFailed {
				want = 200
			}
			if state == protocol.StateExpired {
				want = 410
			}
			if res.StatusCode != want {
				t.Fatalf("owner: %d %s", res.StatusCode, body)
			}
			for _, token := range []string{workerToken, "", "invalid-token"} {
				res, body = e.do("GET", "/v1/jobs/"+status.JobID, token, "1.2.0", nil)
				if res.StatusCode != 401 {
					t.Fatalf("wrong role/credential: %d %s", res.StatusCode, body)
				}
			}
		})
	}
}
