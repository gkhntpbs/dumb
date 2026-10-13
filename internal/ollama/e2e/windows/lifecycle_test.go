//go:build windows

// Package windowse2e verifies the actual official portable Ollama installation
// and low-resource worker lifecycle. Every mutation stays under an explicit root.
package windowse2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
	"github.com/gkhntpbs/dumb/internal/worker"
	"golang.org/x/sys/windows"
)

func TestOfficialWindowsOllamaLifecycle(t *testing.T) {
	if os.Getenv("QA_WINDOWS_OLLAMA") != "1" {
		t.Skip("QA_WINDOWS_OLLAMA=1 authorizes real isolated installation and a small model pull")
	}
	root := os.Getenv("DUMB_OLLAMA_DATA_DIR")
	if root == "" || !filepath.IsAbs(root) {
		t.Fatal("explicit absolute DUMB_OLLAMA_DATA_DIR required")
	}
	model := "smollm2:135m"
	// Refuse any pre-existing server before changing the configured owned root.
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:11434", time.Second)
	if err == nil {
		connection.Close()
		t.Fatal("default endpoint already occupied; no mutation performed")
	}
	for key, value := range map[string]string{"OLLAMA_NUM_PARALLEL": "1", "OLLAMA_MAX_LOADED_MODELS": "1", "OLLAMA_CONTEXT_LENGTH": "512", "CUDA_VISIBLE_DEVICES": "-1", "ROCR_VISIBLE_DEVICES": "-1", "GGML_VK_VISIBLE_DEVICES": "-1", "OLLAMA_VULKAN": "0", "OLLAMA_NO_CLOUD": "1"} {
		t.Setenv(key, value)
	}
	t.Setenv("OLLAMA_MODELS", filepath.Join(root, "models"))
	client, err := ollama.New(ollama.DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	system := ollama.NewSystem(client)
	if detection := system.Detect(context.Background()); detection.Installed {
		t.Fatal("existing installation detected; absent-install case must not overwrite it")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	recovery, err := ollama.NewRecovery(ollama.RecoveryConfig{System: system, Models: []string{model}, Logger: log.New(io.Discard, "", 0), Cooldown: time.Second, Backoff: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := recovery.Close(stop); err != nil {
			t.Errorf("owned cleanup: %v", err)
		}
	}()
	if err := recovery.Ensure(ctx); err != nil {
		t.Fatal("absent install/start/pull", err)
	}
	originalPID := system.OwnedPID()
	if originalPID == 0 {
		t.Fatal("real Ollama is not owned")
	}
	limitCPU(t, originalPID)
	version, err := client.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("official installation ready version=%s model=%s owned=true", version, model)
	if _, err := os.Stat(filepath.Join(root, "Ollama", ".dumb-owned.json")); err != nil {
		t.Fatal(err)
	}
	// Preserve the installed model store across real uninstall/reinstall of only
	// the explicitly owned portable binary directory.
	before, err := client.List(ctx)
	if err != nil || !ollama.HasModel(before, model) {
		t.Fatal("configured model missing", err)
	}
	runQueue(t, ctx, client, recovery, model)
	if err := killOwned(originalPID); err != nil {
		t.Fatal(err)
	}
	await(t, 10*time.Second, "owned crash observed", func() bool { return !system.Owned() })
	if err := recovery.Ensure(ctx); err != nil {
		t.Fatal("owned crash recovery", err)
	}
	nextPID := system.OwnedPID()
	if nextPID == 0 || nextPID == originalPID {
		t.Fatal("owned child was not replaced")
	}
	limitCPU(t, nextPID)
	runQueue(t, ctx, client, recovery, model)
	t.Log("real queue job passed before and after owned process crash/recovery")
	// Another manager must treat this live endpoint as external, including Close.
	observer := ollama.NewSystem(client)
	external, err := ollama.NewRecovery(ollama.RecoveryConfig{System: observer, Models: []string{model}, External: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := external.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := external.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Version(ctx); err != nil || system.OwnedPID() != nextPID {
		t.Fatal("external observer altered owner", err)
	}
	t.Log("existing endpoint preserved by external manager and cancellation")
	if err := recovery.Close(ctx); err != nil {
		t.Fatal(err)
	}
	await(t, 10*time.Second, "owned API stopped", func() bool { _, err := client.Version(context.Background()); return err != nil })
	install := filepath.Join(root, "Ollama")
	if _, err := os.Stat(filepath.Join(install, ".dumb-owned.json")); err != nil {
		t.Fatal("refuse removal without ownership marker", err)
	}
	if err := os.RemoveAll(install); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "models")); err != nil {
		t.Fatal("models removed during owned uninstall", err)
	}
	if err := system.Install(ctx); err != nil {
		t.Fatal("official owned reinstall", err)
	}
	after := ollama.NewSystem(client)
	repeated, err := ollama.NewRecovery(ollama.RecoveryConfig{System: after, Models: []string{model}})
	if err != nil {
		t.Fatal(err)
	}
	defer repeated.Close(context.Background())
	if err := repeated.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	limitCPU(t, after.OwnedPID())
	models, err := client.List(ctx)
	if err != nil || !ollama.HasModel(models, model) {
		t.Fatal("model lost during reinstall", err)
	}
	runQueue(t, ctx, client, repeated, model)
	if err := repeated.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual CLI setup entry point against the retained installation.
	core, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "acceptance-worker.exe")
	build := exec.CommandContext(ctx, "go", "build", "-ldflags", "-H windowsgui", "-o", binary, "./cmd/dumb-worker")
	build.Dir = core
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("CLI build: %v %s", err, output)
	}
	command := exec.CommandContext(ctx, binary, "setup", "-models", model)
	if output, err := command.CombinedOutput(); err != nil || !strings.Contains(string(output), "Setup complete") {
		t.Fatalf("actual CLI setup: %v %s", err, output)
	}
	t.Log("owned uninstall/reinstall preserved downloaded models; actual worker setup passed")
}

func limitCPU(t *testing.T, pid int) {
	t.Helper()
	handle, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetProcessAffinityMask").Call(uintptr(handle), 1); ok == 0 {
		t.Fatal(err)
	}
}
func killOwned(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
func await(t *testing.T, duration time.Duration, label string, check func() bool) {
	t.Helper()
	end := time.Now().Add(duration)
	for time.Now().Before(end) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(label)
}
func credential() string {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes[:])
}
func runQueue(t *testing.T, parent context.Context, client *ollama.Client, recovery *ollama.Recovery, model string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	workerToken, callerToken := credential(), credential()
	tokens := auth.NewStaticTokens()
	tokens.Add("isolated-worker", auth.RoleWorker, workerToken)
	tokens.Add("isolated-caller", auth.RoleCaller, callerToken)
	service, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.0.0", MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(service)
	defer endpoint.Close()
	running, err := worker.New(worker.Config{ServerURL: endpoint.URL, Token: workerToken, Models: []string{model}, Version: "0.0.0", Capacity: 1, Ollama: client, Recovery: recovery, ModelCheck: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- running.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("worker cancellation timed out")
		}
	}()
	await(t, 15*time.Second, "worker announced real model", func() bool { return service.Status().WorkersOnline == 1 })
	body, _ := json.Marshal(protocol.SubmitRequest{Model: model, Prompt: "Reply with one short word.", NumPredict: 8, DeadlineMS: 60000})
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/v1/jobs?wait=60s", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+callerToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result protocol.JobStatus
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || result.State != "done" || strings.TrimSpace(result.Output) == "" {
		t.Fatalf("real generation failed code=%d state=%s error=%s", response.StatusCode, result.State, result.Error)
	}
	t.Log("real DUMB queue generated a nonempty response with a tiny model")
}

func TestWindowsOfficialHTTPS(t *testing.T) {
	if os.Getenv("QA_WINDOWS_OLLAMA") != "1" {
		t.Skip("opt-in real HTTPS connectivity probe")
	}
	for _, url := range []string{ollama.WindowsDownloadURL, "https://registry.ollama.ai/v2/"} {
		request, err := http.NewRequest("HEAD", url, nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Timeout: 30 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			t.Errorf("verified HTTPS %s: %v", url, err)
			continue
		}
		response.Body.Close()
		t.Logf("verified HTTPS host status=%d", response.StatusCode)
	}
}
