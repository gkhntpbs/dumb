package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func keyedSubmit(s *Server, key string, request protocol.SubmitRequest) *httptest.ResponseRecorder {
	body, _ := json.Marshal(request)
	r := httptest.NewRequest("POST", "/v1/jobs", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+callerToken)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestIdempotentSubmitReturnsOriginalJobBeforeCapacityAndAvailability(t *testing.T) {
	e := newEnv(t)
	e.hello()
	first := keyedSubmit(e.srv, "retry-key", submitReq(10000))
	second := keyedSubmit(e.srv, "retry-key", submitReq(10000))
	if first.Code != 202 || second.Code != 202 || first.Body.String() != second.Body.String() || e.srv.q.Pending() != 1 {
		t.Fatalf("duplicate accepted: %d %s / %d %s jobs%d", first.Code, first.Body, second.Code, second.Body, e.srv.q.Pending())
	}
	e.do("POST", "/v1/workers/goodbye", workerToken, "1.2.0", nil)
	replay := keyedSubmit(e.srv, "retry-key", submitReq(10000))
	if replay.Code != 202 || replay.Body.String() != first.Body.String() {
		t.Fatalf("offline replay: %d %s", replay.Code, replay.Body)
	}
	conflictRequest := submitReq(10000)
	conflictRequest.Prompt = "different"
	conflict := keyedSubmit(e.srv, "retry-key", conflictRequest)
	if conflict.Code != 409 {
		t.Fatalf("conflict: %d %s", conflict.Code, conflict.Body)
	}
}

func TestDurableHTTPProcessCrashBeforeResponse(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable server storage requires Linux or macOS")
	}
	if directory := os.Getenv("QA_DURABLE_CRASH_DIRECTORY"); directory != "" {
		e := newEnvWith(t, func(cfg *Config) { cfg.QueueStateDirectory = directory })
		e.hello()
		body, _ := json.Marshal(submitReq(60000))
		request := httptest.NewRequest("POST", "/v1/jobs", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+callerToken)
		request.Header.Set("Idempotency-Key", "lost-response")
		e.srv.ServeHTTP(&blockedDurableResponse{headers: make(http.Header)}, request)
		return
	}
	directory := filepath.Join(t.TempDir(), "state")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestDurableHTTPProcessCrashBeforeResponse$")
	command.Env = append(os.Environ(), "QA_DURABLE_CRASH_DIRECTORY="+directory)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			command.Process.Kill()
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "durable-response-blocked" {
		command.Wait()
		t.Fatalf("child did not reach durable boundary: %s %v %s", scanner.Text(), scanner.Err(), stderr.String())
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("child exited without crash")
	}
	e := newEnvWith(t, func(cfg *Config) { cfg.QueueStateDirectory = directory })
	t.Cleanup(func() { e.srv.Close() })
	if e.srv.q.Pending() != 1 {
		t.Fatal("accepted job lost after crash")
	}
	before := e.srv.q.Observe("", "", 0, 10).Jobs[0]
	replay := keyedSubmit(e.srv, "lost-response", submitReq(60000))
	if replay.Code != 202 || e.srv.q.Pending() != 1 {
		t.Fatalf("ambiguous retry duplicated: %d %s", replay.Code, replay.Body)
	}
	var result protocol.JobStatus
	if err = json.Unmarshal(replay.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.JobID != before.JobID {
		t.Fatal("replayed another job")
	}
	deadline, err := e.srv.q.Deadline("app", result.JobID)
	if err != nil || !deadline.Equal(before.DeadlineAt) {
		t.Fatalf("deadline changed: %v %v", deadline, err)
	}
}

type blockedDurableResponse struct{ headers http.Header }

func (w *blockedDurableResponse) Header() http.Header { return w.headers }
func (w *blockedDurableResponse) WriteHeader(code int) {
	fmt.Println("durable-response-blocked")
	select {}
}
func (w *blockedDurableResponse) Write(data []byte) (int, error) {
	return 0, errors.New("response must remain blocked")
}

func TestIdempotencyHeaderValidationAndNormalization(t *testing.T) {
	e := newEnv(t)
	e.hello()
	for _, key := range []string{"has space", "!bad", strings.Repeat("x", 65)} {
		response := keyedSubmit(e.srv, key, submitReq(60000))
		if response.Code != 400 {
			t.Fatalf("invalid key%q: %d %s", key, response.Code, response.Body)
		}
	}
	first := keyedSubmit(e.srv, "normalized", submitReq(60000))
	request := submitReq(60000)
	request.NumPredict = protocol.MaxNumPredict
	second := keyedSubmit(e.srv, "normalized", request)
	if first.Code != 202 || first.Body.String() != second.Body.String() {
		t.Fatal("equivalent token defaults created another job")
	}
}
