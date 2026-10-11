package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) lines(t *testing.T) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestStructuredLogging(t *testing.T) {
	buf := &syncBuf{}
	e := newEnvWith(t, func(c *Config) {
		c.Logger = slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c.WorkerRate = Rate{PerSecond: 0.01, Burst: 6}
	})
	const prompt, system, output = "PROMPT-must-not-be-logged", "SYSTEM-must-not-be-logged", "OUTPUT-must-not-be-logged"
	e.helloCap(1)

	// A sane incoming request id is kept and echoed; an insane one is replaced.
	req, _ := http.NewRequest("GET", e.ts.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+callerToken)
	req.Header.Set(RequestIDHeader, "trace-42.a:b")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get(RequestIDHeader); got != "trace-42.a:b" {
		t.Fatalf("request id echoed as %q", got)
	}
	req.Header.Set(RequestIDHeader, "bad id "+strings.Repeat("x", 80))
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if got := res.Header.Get(RequestIDHeader); !saneRequestID.MatchString(got) || strings.Contains(got, "bad") {
		t.Fatalf("insane request id not replaced: %q", got)
	}

	// submit, claim, result
	go e.do("POST", "/v1/jobs?wait=1s", callerToken, "", protocol.SubmitRequest{Model: model, Prompt: prompt, System: system, DeadlineMS: 5000})
	c, a := e.claim("1s")
	if c != 200 {
		t.Fatalf("claim: %d", c)
	}
	e.do("POST", "/v1/jobs/"+a.JobID+"/result", workerToken, "1.2.0", protocol.ResultRequest{LeaseID: a.LeaseID, Output: output})
	// expiry of a queued job
	e.do("POST", "/v1/jobs", callerToken, "", protocol.SubmitRequest{Model: model, Prompt: prompt, DeadlineMS: 30})
	// 401, 426, 429
	e.do("GET", "/v1/status", "wrong-token-must-not-be-logged", "", nil)
	e.do("GET", "/v1/workers/me", workerToken, "1.0.0", nil)
	for range 3 {
		e.do("GET", "/v1/workers/me", workerToken, "1.2.0", nil)
	}
	time.Sleep(100 * time.Millisecond) // let the expiry fire

	lines := buf.lines(t)
	want := map[string]string{ // msg → level
		"worker hello": "INFO", "job submitted": "INFO", "job claimed": "INFO", "job result": "INFO",
		"job expired": "WARN", "unauthorized": "WARN", "upgrade required": "INFO", "rate limited": "WARN", "request": "DEBUG",
	}
	seen := map[string]bool{}
	for _, l := range lines {
		msg, _ := l["msg"].(string)
		lvl, ok := want[msg]
		if !ok {
			continue
		}
		seen[msg] = true
		if l["level"] != lvl {
			t.Errorf("%q logged at %v, want %s", msg, l["level"], lvl)
		}
		if msg != "job expired" && l["request_id"] == nil {
			t.Errorf("%q has no request_id: %v", msg, l)
		}
		switch msg {
		case "job claimed":
			if l["prompt_bytes"] != float64(len(prompt)) || l["system_bytes"] != float64(len(system)) || l["job_id"] != a.JobID {
				t.Errorf("claim line: %v", l)
			}
		case "job result":
			if l["output_bytes"] != float64(len(output)) || l["state"] != "done" {
				t.Errorf("result line: %v", l)
			}
		}
	}
	for msg := range want {
		if !seen[msg] {
			t.Errorf("no %q line in:\n%s", msg, buf)
		}
	}
	for _, secret := range []string{prompt, system, output, workerToken, callerToken, "wrong-token-must-not-be-logged"} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
	}
	// The trace id travels on its line.
	if !strings.Contains(buf.String(), `"request_id":"trace-42.a:b"`) {
		t.Error("incoming request id not used in the log")
	}
}
