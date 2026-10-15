package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
)

func credential() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// workerID looks like a production worker id: 43 random base64url characters.
func workerID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	a := auth.NewStaticTokens()
	password := os.Getenv("QA_ADMIN_PASSWORD")
	if file := os.Getenv("QA_ADMIN_PASSWORD_FILE"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read QA password file: %w", err)
		}
		password = strings.TrimSpace(string(data))
	}
	if password == "" {
		return fmt.Errorf("set QA_ADMIN_PASSWORD or QA_ADMIN_PASSWORD_FILE before starting the preview")
	}
	sessions := &fixtureSessions{password: sha256.Sum256([]byte(password)), sessions: make(map[string]time.Time)}
	caller, workerA, workerB, workerC, workerD := credential(), credential(), credential(), credential(), credential()
	a.Add("qa-caller", auth.RoleCaller, caller)
	// Display names: a normal one, none (a worker from before names), one at
	// the 64-character limit, and a short one.
	names := map[string]string{
		workerA: "Studio Mac mini",
		workerB: "",
		workerC: "Research lab workstation with an exceptionally long display name",
		workerD: "Living room Pi 5",
	}
	for _, token := range []string{workerA, workerB, workerC, workerD} {
		a.Add(workerID(), auth.RoleWorker, token)
	}
	var fixtureOffset time.Duration
	s, err := server.New(server.Config{Now: func() time.Time { return time.Now().Add(fixtureOffset) }, Auth: a, AdminSessions: sessions, MinWorkerVersion: "0.1.0", OnlineTTL: 24 * time.Hour})
	if err != nil {
		return err
	}
	request := func(method, path, token string, payload any, out any) error {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(protocol.WorkerVersionHeader, "0.1.0")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code < 200 || w.Code >= 300 {
			return fmt.Errorf("preview fixture %s: HTTP %d", path, w.Code)
		}
		if out != nil {
			return json.Unmarshal(w.Body.Bytes(), out)
		}
		return nil
	}
	hello := func(token string) error {
		return request("POST", "/v1/workers/hello", token, protocol.HelloRequest{WorkerVersion: "0.1.0", WorkerName: names[token], ProtocolVersion: protocol.Version, Models: []string{"smollm2:135m", "llama3.2:3b"}, Capacity: 3}, nil)
	}
	for _, token := range []string{workerA, workerB, workerD, workerC} {
		if token == workerC {
			fixtureOffset = -25 * time.Hour
		}
		if err := hello(token); err != nil {
			return err
		}
	}
	fixtureOffset = 0
	var queuedIDs []string
	var submitted protocol.JobStatus
	for i := 0; i < 8; i++ {
		model := "smollm2:135m"
		if i == 7 {
			model = "llama3.2:3b"
		}
		if err := request("POST", "/v1/jobs", caller, protocol.SubmitRequest{Model: model, Prompt: "isolated preview fixture", DeadlineMS: 600000}, &submitted); err != nil {
			return err
		}
		queuedIDs = append(queuedIDs, submitted.JobID)
	}
	for i := 0; i < 4; i++ {
		var assignment protocol.Assignment
		if err := request("GET", "/v1/jobs/next?wait=0", workerA, nil, &assignment); err != nil {
			return err
		}
		if i < 2 {
			result := protocol.ResultRequest{LeaseID: assignment.LeaseID, Output: "not exposed to admin"}
			if i == 1 {
				result.Output = ""
				result.Error = "fixture failure"
			}
			if err := request("POST", "/v1/jobs/"+assignment.JobID+"/result", workerA, result, nil); err != nil {
				return err
			}
		}
		if i == 3 {
			// One leased and one queued job show the cancelled state.
			if err := request("POST", "/v1/jobs/"+assignment.JobID+"/cancel", caller, nil, nil); err != nil {
				return err
			}
		}
	}
	if err := request("POST", "/v1/jobs/"+queuedIDs[6]+"/cancel", caller, nil, nil); err != nil {
		return err
	}
	address := os.Getenv("QA_ADMIN_LISTEN")
	if address == "" {
		address = "127.0.0.1:52147"
	}
	listener, err := server.ListenAdmin(address)
	if err != nil {
		return err
	}
	defer listener.Close()
	h := &http.Server{Handler: s.AdminHandler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- h.Serve(listener) }()
	// Keep the API charts populated by real, harmless public-handler traffic.
	// This fixture never opens a public listener or contacts a user device.
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, token := range []string{workerA, workerB, workerD} {
					_ = hello(token)
				}
				_ = request("GET", "/v1/jobs/fixture-missing", caller, nil, nil)
				_ = request("POST", "/v1/jobs", "", protocol.SubmitRequest{}, nil)
			}
		}
	}()
	fmt.Printf("Admin preview ready: http://%s\nSign in with your configured QA password. No host Ollama, models or services are used. Ctrl-C stops the preview.\n", listener.Addr())
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.Shutdown(shutdown)
	case err := <-done:
		return err
	}
}

// fixtureSessions is intentionally preview-only and never a production password store.
type fixtureSessions struct {
	mu       sync.Mutex
	password [32]byte
	sessions map[string]time.Time
}

func (f *fixtureSessions) Authenticate(r *http.Request) (auth.Principal, error) {
	c, err := r.Cookie("dumb-preview-session")
	if err != nil {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	expiry, ok := f.sessions[auth.HashToken(c.Value)]
	if !ok || !time.Now().Before(expiry) {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	return auth.Principal{ID: "qa-admin", Role: auth.RoleAdminRead}, nil
}
func (f *fixtureSessions) Login(w http.ResponseWriter, r *http.Request, password string) error {
	candidate := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(candidate[:], f.password[:]) != 1 {
		return auth.ErrUnauthorized
	}
	value := credential()
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, err := r.Cookie("dumb-preview-session"); err == nil {
		delete(f.sessions, auth.HashToken(old.Value))
	}
	for key, expiry := range f.sessions {
		if !time.Now().Before(expiry) {
			delete(f.sessions, key)
		}
	}
	f.sessions[auth.HashToken(value)] = time.Now().Add(time.Hour)
	http.SetCookie(w, &http.Cookie{Name: "dumb-preview-session", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 3600})
	return nil
}
func (f *fixtureSessions) Logout(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, err := r.Cookie("dumb-preview-session"); err == nil {
		delete(f.sessions, auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "dumb-preview-session", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}
