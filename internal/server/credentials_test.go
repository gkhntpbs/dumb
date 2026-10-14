package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

type fixtureCredentials struct {
	calls int
	err   error
	code  string
}

func (f *fixtureCredentials) List(context.Context, int, int) (protocol.CredentialPage, error) {
	f.calls++
	return protocol.CredentialPage{Items: []protocol.Credential{}}, f.err
}
func (f *fixtureCredentials) Issue(context.Context, protocol.IssueCredentialRequest) (protocol.IssuedCredential, error) {
	f.calls++
	return protocol.IssuedCredential{}, f.err
}
func (f *fixtureCredentials) Rotate(context.Context, string, protocol.RotateCredentialRequest) (protocol.IssuedCredential, error) {
	f.calls++
	return protocol.IssuedCredential{}, f.err
}
func (f *fixtureCredentials) Revoke(context.Context, string) error { f.calls++; return f.err }
func (f *fixtureCredentials) CreatePairing(context.Context, protocol.CreatePairingRequest) (protocol.PairingCode, error) {
	f.calls++
	return protocol.PairingCode{}, f.err
}
func (f *fixtureCredentials) ExchangePairing(_ context.Context, code string) (protocol.IssuedCredential, error) {
	f.calls++
	if code != f.code {
		return protocol.IssuedCredential{}, ErrPairingDenied
	}
	return protocol.IssuedCredential{}, f.err
}

func TestCredentialBoundary(t *testing.T) {
	f := &fixtureCredentials{}
	sessions := &fixtureAdminSessions{role: auth.RoleAdminManage}
	e := newEnvWith(t, func(c *Config) {
		c.Credentials = f
		c.AdminOrigin = "https://admin.invalid"
		c.AdminSessions = sessions
	})
	h := e.srv.AdminHandler()
	call := func(method, path, body, origin, typ string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://admin.invalid"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", typ)
		r.Header.Set("Authorization", "Bearer "+workerToken)
		if cookie {
			r.AddCookie(&http.Cookie{Name: "dumb_admin_session", Value: randomAdminToken()})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cached")
		}
		return w
	}
	for _, o := range []string{"", "null", "http://admin.invalid", "https://admin.invalid/", "https://evil.invalid"} {
		if w := call("GET", "/admin/v1/credentials", "", o, "", true); w.Code != 403 {
			t.Fatalf("origin %q: %d", o, w.Code)
		}
	}
	if f.calls != 0 {
		t.Fatal("origin reached provider")
	}
	if w := call("GET", "/admin/v1/credentials", "", "https://admin.invalid", "", false); w.Code != 401 {
		t.Fatal("bearer bypass", w.Code)
	}
	sessions.role = auth.RoleAdminRead
	if w := call("GET", "/admin/v1/credentials", "", "https://admin.invalid", "", true); w.Code != 401 {
		t.Fatal("read mutated", w.Code)
	}
	sessions.role = auth.RoleAdminManage
	if w := call("GET", "/admin/v1/summary", "", "https://admin.invalid", "", true); w.Code != 200 {
		t.Fatal("manage cannot read", w.Code)
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"role":"admin-manage","ttl_seconds":10}`, `{"role":"worker","ttl_seconds":0}`, `{"role":"worker","ttl_seconds":31622401}`, `{"role":"worker","ttl_seconds":1,"extra":1}`, `{"role":"worker","ttl_seconds":1} {}`} {
		if w := call("POST", "/admin/v1/credentials", body, "https://admin.invalid", "application/json", true); w.Code != 400 {
			t.Fatalf("body %s: %d", body, w.Code)
		}
	}
	if w := call("POST", "/admin/v1/credentials", `{"role":"caller","ttl_seconds":1}`, "https://admin.invalid", "text/plain", true); w.Code != 415 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/admin/v1/pairing", `{"ttl_seconds":601,"credential_ttl_seconds":1}`, "https://admin.invalid", "application/json", true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/admin/v1/credentials", `{"role":"caller","ttl_seconds":31622400}`, "https://admin.invalid", "application/json", true); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`null`, `[]`, `{"extra":true}`, `{} {}`} {
		if w := call("POST", "/admin/v1/credentials/fixture/revoke", body, "https://admin.invalid", "application/json", true); w.Code != 400 {
			t.Fatalf("revoke %s: %d", body, w.Code)
		}
	}
	f.err = errors.New("private backend detail")
	if w := call("GET", "/admin/v1/credentials", "", "https://admin.invalid", "", true); w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("unsanitized", w.Code, w.Body.String())
	}
}

func TestPairingIsSeparateBoundedRateAndVersion(t *testing.T) {
	f := &fixtureCredentials{code: strings.Repeat("a", 43)}
	e := newEnvWith(t, func(c *Config) {
		c.Credentials = f
		c.AdminOrigin = "https://admin.invalid"
		c.PairingRate = Rate{PerSecond: 0.00001, Burst: 3}
	})
	call := func(body, version string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://api.invalid/v1/workers/pair", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(protocol.WorkerVersionHeader, version)
		w := httptest.NewRecorder()
		e.srv.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cached")
		}
		return w
	}
	if w := call(`{"code":"`+f.code+`"}`, "1.1.0"); w.Code != 426 {
		t.Fatal(w.Code)
	}
	if w := call(`{"code":"short"}`, "1.2.0"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call(`{"code":"`+f.code+`"}`, "1.2.0"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(`{"code":"`+f.code+`"}`, "1.2.0"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code)
	}
	if f.calls != 1 {
		t.Fatal(f.calls)
	}
	// Pair attempt exhaustion does not poison valid worker auth behind the same IP.
	r := httptest.NewRequest("GET", "http://api.invalid/v1/workers/me", nil)
	r.Header.Set("Authorization", "Bearer "+workerToken)
	r.Header.Set(protocol.WorkerVersionHeader, "1.2.0")
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestRevokeAfterClaimReleasesDurably(t *testing.T) {
	for _, kind := range []string{"revoke", "rotate", "expire", "change-identity", "change-role"} {
		t.Run(kind, func(t *testing.T) { testInvalidAfterClaimReleasesDurably(t, kind) })
	}
}
func testInvalidAfterClaimReleasesDurably(t *testing.T, kind string) {
	e := newEnvWith(t, func(c *Config) {
		c.QueueStateDirectory = t.TempDir()
		if err := os.Chmod(c.QueueStateDirectory, 0700); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(func() { e.srv.Close() })
	e.hello()
	e.srv.afterClaim = func(*http.Request) {
		switch kind {
		case "revoke":
			e.tokens.Revoke("w1")
		case "rotate":
			e.tokens.Add("w1", auth.RoleWorker, randomAdminToken())
		case "expire":
			e.tokens.AddUntil("w1", auth.RoleWorker, workerToken, time.Now().Add(-time.Second))
		case "change-identity":
			e.tokens.Add("other-worker", auth.RoleWorker, workerToken)
		case "change-role":
			e.tokens.Add("w1", auth.RoleCaller, workerToken)
		}
	}
	res, body := e.do("POST", "/v1/jobs", callerToken, "", protocol.SubmitRequest{Model: model, Prompt: "fixture", DeadlineMS: 30000})
	if res.StatusCode != 202 {
		t.Fatal(res.StatusCode, string(body))
	}
	res, body = e.do("GET", "/v1/jobs/next?wait=1s", workerToken, "1.2.0", nil)
	if res.StatusCode != 401 {
		t.Fatal("revoked got assignment", res.StatusCode, string(body))
	}
	if e.srv.q.Leases("w1") != 0 || e.srv.q.Pending() != 1 {
		t.Fatal("lease retained")
	}
	dir := e.srv.cfg.QueueStateDirectory
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{Auth: e.tokens, MinWorkerVersion: "1.2.0", QueueStateDirectory: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.q.Pending() != 1 || reopened.q.Leases("w1") != 0 {
		t.Fatal("release not durable")
	}
}

func TestPairingAttemptMapBounded(t *testing.T) {
	lim := newLimiter(Rate{PerSecond: 0.5, Burst: 10}, time.Now)
	lim.maxKeys = 2
	if ok, _ := lim.allow("fixture-a"); !ok {
		t.Fatal("first denied")
	}
	if ok, _ := lim.allow("fixture-b"); !ok {
		t.Fatal("second denied")
	}
	if ok, _ := lim.allow("fixture-c"); ok {
		t.Fatal("unbounded key")
	}
	if len(lim.b) != 2 {
		t.Fatal(len(lim.b))
	}
	if ok, _ := lim.allow("fixture-a"); !ok {
		t.Fatal("existing key denied")
	}
}
func TestManagementConfigRejectsInexactOrigin(t *testing.T) {
	for _, origin := range []string{"", "https://admin.invalid/", "https://user@admin.invalid", "https://admin.invalid?x=1", "https://admin.invalid#fragment", "https://admin.invalid?", "https://admin.invalid#"} {
		_, err := New(Config{Auth: auth.NewStaticTokens(), MinWorkerVersion: "1.2.0", Credentials: &fixtureCredentials{}, AdminOrigin: origin})
		if err == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
}

func TestRevokedParkedPollCannotReceiveNewJob(t *testing.T) {
	e := newEnv(t)
	e.hello()
	done := make(chan int, 1)
	r, err := http.NewRequest("GET", e.ts.URL+"/v1/jobs/next?wait=2s", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+workerToken)
	r.Header.Set(protocol.WorkerVersionHeader, "1.2.0")
	go func() {
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			done <- 0
			return
		}
		res.Body.Close()
		done <- res.StatusCode
	}()
	deadline := time.Now().Add(time.Second)
	for {
		e.srv.mu.Lock()
		polling := e.srv.workers["w1"].polling
		e.srv.mu.Unlock()
		if polling > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("poll did not park")
		}
		time.Sleep(time.Millisecond)
	}
	e.tokens.Revoke("w1")
	res, body := e.do("POST", "/v1/jobs", callerToken, "", protocol.SubmitRequest{Model: model, Prompt: "fixture", DeadlineMS: 30000})
	if res.StatusCode != 202 {
		t.Fatal(res.StatusCode, string(body))
	}
	select {
	case status := <-done:
		if status != 401 {
			t.Fatal("parked revoked bearer got job", status)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poll stuck")
	}
	if e.srv.q.Pending() != 1 || e.srv.q.Leases("w1") != 0 {
		t.Fatal("undelivered job was not released")
	}
}
