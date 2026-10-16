package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
)

func TestMacDesktopApplicationEndToEnd(t *testing.T) {
	app := os.Getenv("QA_MACOS_APP")
	if app == "" {
		t.Skip("QA_MACOS_APP selects an explicit native application fixture")
	}
	executable := filepath.Join(app, "Contents", "MacOS", "dumb-worker")
	if !filepath.IsAbs(app) {
		t.Fatal("QA_MACOS_APP must be absolute")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "Library", "Application Support", "DUMB", "Worker")
	if err := macPrivateRoot(root); err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"version":"0.0.0-fixture"}`))
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"fixture"}]}`))
		case "/api/generate":
			w.Write([]byte(`{"response":"sealed fixture output","done":true}`))
		default:
			t.Error("unexpected backend request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	token := func() string {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(raw)
	}
	workerToken, callerToken := token(), token()
	tokens := auth.NewStaticTokens()
	tokens.Add("fixture-worker", auth.RoleWorker, workerToken)
	tokens.Add("fixture-caller", auth.RoleCaller, callerToken)
	queue, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(queue)
	defer endpoint.Close()
	saved := desktopConfig{Server: endpoint.URL, Models: "fixture", OllamaURL: backend.URL}
	data, _ := json.Marshal(saved)
	if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "token"), []byte(workerToken), 0600); err != nil {
		t.Fatal(err)
	}
	environment := []string{}
	for _, entry := range os.Environ() {
		name := strings.SplitN(entry, "=", 2)[0]
		if name != "HOME" && !strings.HasPrefix(name, "DUMB_") && !strings.HasPrefix(name, "OLLAMA_") && !strings.HasPrefix(name, "QA_") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "HOME="+home, "QA_MENU_DIAGNOSTICS="+filepath.Join(root, "menu.jsonl"))
	command := exec.Command(executable, "desktop")
	command.Env = environment
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			command.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(25 * time.Second):
				command.Process.Kill()
				<-done
			}
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for queue.Status().WorkersOnline != 1 {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("application exited before ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(filepath.Join(root, "worker.log"))
			t.Fatalf("application readiness timed out: %s", log)
		}
		time.Sleep(20 * time.Millisecond)
	}
	duplicate := exec.Command(executable, "desktop")
	duplicate.Env = environment
	if err := duplicate.Run(); err != nil {
		t.Fatalf("duplicate launch: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, _ := json.Marshal(protocol.SubmitRequest{Model: "fixture", Prompt: "fixture", DeadlineMS: 2000})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL+"/v1/jobs?wait=2s", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+callerToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var job protocol.JobStatus
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&job) != nil || job.State != "done" || job.Output != "sealed fixture output" {
		t.Fatalf("bundled worker job failed: status=%d state=%s", response.StatusCode, job.State)
	}
	if data, err := os.ReadFile(filepath.Join(root, "menu.jsonl")); err != nil || !bytes.Contains(data, []byte(`"state":"ready"`)) {
		t.Fatal("native helper did not publish ready")
	}
	command.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		stopped = true
	case <-time.After(25 * time.Second):
		t.Fatal("application did not stop")
	}
	if queue.Status().WorkersOnline != 0 {
		t.Fatal("application did not send goodbye")
	}
}
