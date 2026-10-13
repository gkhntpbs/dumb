//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/server"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/worker"
	"golang.org/x/sys/windows"
)

func init() {
	if os.Getenv("QA_DESKTOP_CHILD") != "1" || len(os.Args) < 2 {
		return
	}
	root, _ := desktopRoot()
	if os.Args[1] == "desktop" {
		nonce := ""
		if len(os.Args) > 2 {
			nonce = os.Args[2]
		}
		os.Exit(desktopSupervisor(context.Background(), root, nonce))
	}
	if os.Args[1] != "desktop-worker" {
		return
	}
	counter := filepath.Join(root, "child-count")
	previous, _ := os.ReadFile(counter)
	os.WriteFile(counter, append(previous, 'x'), 0600)
	if len(previous) == 0 {
		exe := filepath.Join(root, "dumb-worker.exe")
		data, err := os.ReadFile(exe)
		if err != nil {
			os.Exit(exitError)
		}
		if os.Rename(exe, exe+".prev") != nil {
			os.Exit(exitError)
		}
		if os.WriteFile(exe, data, 0700) != nil {
			os.Exit(exitError)
		}
		os.Exit(exitRestart)
	}
	platformReady()
	event, err := windows.OpenEvent(windows.SYNCHRONIZE, false, desktopObject(root, "stop"))
	if err != nil {
		os.Exit(exitError)
	}
	for {
		result, _ := windows.WaitForSingleObject(event, 100)
		if result == windows.WAIT_OBJECT_0 {
			windows.CloseHandle(event)
			os.Exit(exitOK)
		}
	}
}

func TestDesktopCredentialUsesExistingProtocolWithoutFollowingRedirects(t *testing.T) {
	const token = "inert-onboarding-worker-token"
	for _, code := range []int{200, 401, 426, 302} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/workers/me" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get(protocol.WorkerVersionHeader) != version {
					t.Error("existing credential contract changed")
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(code)
				json.NewEncoder(w).Encode(protocol.WhoAmI{WorkerID: "fixture-worker"})
			}))
			defer server.Close()
			err := desktopCredential(context.Background(), server.URL, token)
			if (err == nil) != (code == 200) {
				t.Fatalf("HTTP %d accepted=%v", code, err == nil)
			}
			if calls != 1 {
				t.Fatal("credential followed redirect")
			}
			if err != nil && strings.Contains(err.Error(), token) {
				t.Fatal("credential leaked in error")
			}
		})
	}
}

func TestDesktopConfigurationIsRestrictedAndNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	if err := restrictDesktopPath(root); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUMB_OLLAMA_DATA_DIR", "")
	saved := desktopConfig{Server: "http://127.0.0.1:18080", Models: "fixture:model", OllamaDataDir: filepath.Join(t.TempDir(), "owned")}
	if err := installDesktop(root, saved, "inert-token"); err != nil {
		t.Fatal(err)
	}
	c, err := loadDesktopConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.server != saved.Server || c.models != saved.Models || c.ollamaSystem(nil).WindowsDataDir != saved.OllamaDataDir {
		t.Fatal("configuration persistence changed")
	}
	if _, err := worker.ReadTokenFile(c.tokenFile); err != nil {
		t.Fatal(err)
	}
	if err := installDesktop(root, saved, "replacement-token"); err == nil {
		t.Fatal("existing installation overwritten")
	}
	original, _ := os.ReadFile(c.tokenFile)
	if string(original) != "inert-token" {
		t.Fatal("existing credential modified")
	}
	if err := desktopWrite(filepath.Join(root, "token"), []byte("replace"), 0600); err == nil {
		t.Fatal("exclusive write overwrote credential")
	}
}

func TestDesktopSupervisorRestartsOnlyExitFourAndStopsItsOwnChild(t *testing.T) {
	testDesktopSupervisorLifecycle(t, startDesktop)
}

func TestDesktopStartupCommandUsesPrivateSupervisorCopy(t *testing.T) {
	testDesktopSupervisorLifecycle(t, func(ctxRoot string, wait bool) error {
		if code := platformRun(context.Background(), []string{"desktop"}, io.Discard, io.Discard); code != exitOK {
			return fmt.Errorf("startup command failed: %d", code)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			markers, _ := filepath.Glob(filepath.Join(ctxRoot, "ready-*"))
			for _, marker := range markers {
				if data, err := os.ReadFile(marker); err == nil && string(data) == strings.TrimPrefix(filepath.Base(marker), "ready-") {
					return nil
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		return fmt.Errorf("startup command did not confirm copied supervisor readiness")
	})
}

func testDesktopSupervisorLifecycle(t *testing.T, launch func(string, bool) error) {
	t.Helper()
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("QA_DESKTOP_CHILD", "1")
	root, err := desktopRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "dumb-worker.exe"), data, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { rollbackDesktop(root) })
	if err := launch(root, true); err != nil {
		t.Fatal(err)
	}
	count, _ := os.ReadFile(filepath.Join(root, "child-count"))
	if string(count) != "xx" {
		t.Fatal("exit4 did not start the changed worker")
	}
	if err := os.Remove(filepath.Join(root, "dumb-worker.exe.prev")); err != nil {
		t.Fatalf("resident supervisor kept the previous stable image mapped: %v", err)
	}
	t.Log("PASS: resident byte-identical supervisor copy does not hold the previous stable worker image; native .prev deletion succeeds after exit4 restart")
	if code := desktopSupervisor(ctx, root, strings.Repeat("1", 32)); code != exitOK {
		t.Fatal("duplicate launch failed")
	}
	rollbackDesktop(root)
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, desktopObject(root, "desktop"))
	if err == nil {
		windows.CloseHandle(handle)
		t.Fatal("owned supervisor remained after scoped stop")
	}

	count, _ = os.ReadFile(filepath.Join(root, "child-count"))
	if string(count) != "xx" {
		t.Fatal("duplicate or normal stop restarted child")
	}
	matches, _ := filepath.Glob(filepath.Join(root, "ready-*"))
	if len(matches) != 0 {
		t.Fatal("readiness marker not cleaned")
	}
}

func TestDesktopSupervisorRejectsReadinessPathTraversal(t *testing.T) {
	if code := desktopSupervisor(context.Background(), t.TempDir(), "../outside"); code != exitUsage {
		t.Fatal("readiness path traversal accepted")
	}
}

func TestDesktopActualSingleExecutableTraySurvivesLauncherExit(t *testing.T) {
	if os.Getenv("QA_WINDOWS_DESKTOP") != "1" {
		t.Skip("set QA_WINDOWS_DESKTOP=1 for isolated actual tray worker acceptance")
	}
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	root, _ := desktopRoot()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := restrictDesktopPath(root); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "dumb-worker.exe")
	build := exec.Command("go", "build", "-trimpath", "-ldflags", "-H windowsgui -X main.version=0.1.0", "-o", exe, "./cmd/dumb-worker")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual worker: %v %s", err, output)
	}
	tokens := auth.NewStaticTokens()
	tokens.Add("desktop-worker", auth.RoleWorker, "inert-desktop-worker")
	tokens.Add("desktop-caller", auth.RoleCaller, "inert-desktop-caller")
	api, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(api)
	defer backend.Close()
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"desktop-fixture"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"desktop:model"}]}`)
		case "/api/generate":
			fmt.Fprintln(w, `{"response":"Desktop job completed","done":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollama.Close()
	t.Setenv("DUMB_OLLAMA", ollama.URL)
	data, _ := json.Marshal(desktopConfig{Server: backend.URL, Models: "desktop:model"})
	if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "token"), []byte("inert-desktop-worker"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restrictDesktopPath(filepath.Join(root, "token")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rollbackDesktop(root) })
	launcher := exec.Command(exe)
	launcher.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := launcher.Run(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(root, "ready-*"))
		if len(matches) == 1 && api.Status().WorkersOnline == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if api.Status().WorkersOnline != 1 {
		logdata, _ := os.ReadFile(filepath.Join(root, "worker.log"))
		if strings.Contains(string(logdata), "desktop status UI unavailable") {
			t.Log("native diagnosis: same-exe tray was unavailable in this non-interactive SSH session")
		}
		t.Fatal("actual detached worker never announced hello")
	}
	matches, _ := filepath.Glob(filepath.Join(root, "ready-*"))
	if len(matches) != 1 {
		t.Fatal("tray handshake and real hello did not publish readiness")
	}
	if _, err := os.Stat(filepath.Join(root, "dumb-tray.exe")); !os.IsNotExist(err) {
		t.Fatal("acceptance relied on sibling helper")
	}
	payload, _ := json.Marshal(protocol.SubmitRequest{Model: "desktop:model", Prompt: "inert desktop acceptance", DeadlineMS: 5000})
	req, _ := http.NewRequest(http.MethodPost, backend.URL+"/v1/jobs?wait=3s", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer inert-desktop-caller")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var result protocol.JobStatus
	err = json.NewDecoder(response.Body).Decode(&result)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || result.State != protocol.StateDone || result.Output != "Desktop job completed" {
		t.Fatal("actual desktop worker did not serve after launcher exited")
	}
	t.Log("PASS: self-contained GUI worker launcher exited; detached supervisor and same-exe tray handshake reached actual hello and served fixture job without sibling helper")
	rollbackDesktop(root)
	if api.Status().WorkersOnline != 0 {
		t.Fatal("scoped desktop stop did not send goodbye")
	}
	t.Log("PASS: scoped desktop stop removed supervisor/readiness and worker went offline")
}

func secureDesktopTestRoot(root string) error { return restrictDesktopPath(root) }
