//go:build windows

// Package windowsscm verifies a uniquely named, manually started test service.
package windowsscm

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWindowsSCMWorker(t *testing.T) {
	if os.Getenv("QA_WINDOWS_SCM") != "1" {
		t.Skip("set QA_WINDOWS_SCM=1 to create/start/stop/delete one isolated LocalSystem test service")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	exe := filepath.Join(temp, "dumb-worker.exe")
	cmd := exec.Command("go", "build", "-trimpath", "-o", exe, "-ldflags", "-H windowsgui -X main.version=0.1.0", "./cmd/dumb-worker")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v\n%s", err, output)
	}
	tokenFile := filepath.Join(temp, "token")
	if err := os.WriteFile(tokenFile, []byte("inert-scm-worker-token"), 0600); err != nil {
		t.Fatal(err)
	}
	protectSystemToken(t, tokenFile)
	tokens := auth.NewStaticTokens()
	tokens.Add("scm-worker", auth.RoleWorker, "inert-scm-worker-token")
	tokens.Add("scm-caller", auth.RoleCaller, "inert-scm-caller-token")
	api, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var goodbye, activePoll, canceledPoll atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workers/goodbye" {
			goodbye.Add(1)
		}
		if r.URL.Path == "/v1/jobs/next" {
			activePoll.Add(1)
			defer activePoll.Add(-1)
			defer func() {
				if r.Context().Err() != nil {
					canceledPoll.Add(1)
				}
			}()
		}
		api.ServeHTTP(w, r)
	}))
	defer backend.Close()
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"isolated-scm-fixture"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"scm:model"}]}`)
		case "/api/generate":
			fmt.Fprint(w, `{"response":"SCM job completed","done":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollama.Close()
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Disconnect()
	entropy := make([]byte, 8)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	name := "DUMB-QA-" + hex.EncodeToString(entropy)
	args := []string{"run", "-headless", "-server", backend.URL, "-token-file", tokenFile, "-models", "scm:model", "-ollama", ollama.URL}
	installed, err := manager.CreateService(name, exe, mgr.Config{ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: mgr.StartManual, ErrorControl: mgr.ErrorNormal, ServiceStartName: "LocalSystem", DisplayName: name, Description: "Temporary isolated DUMB worker acceptance fixture"}, args...)
	if err != nil {
		t.Fatal(err)
	}
	deleted, closed := false, false
	// Even after an assertion fails, operate only on this newly created handle.
	defer func() {
		if !deleted {
			_, _ = installed.Control(svc.Stop)
			deadline := time.Now().Add(15 * time.Second)
			stopped := false
			for time.Now().Before(deadline) {
				status, e := installed.Query()
				if e == nil && status.State == svc.Stopped {
					stopped = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !stopped {
				t.Errorf("owned service %s failed to stop; retained for explicit inspection", name)
			} else if e := installed.Delete(); e != nil {
				t.Errorf("delete exact owned service %s: %v", name, e)
			}
		}
		if !closed {
			_ = installed.Close()
		}
	}()
	if err := installed.Start(); err != nil {
		t.Fatal(err)
	}
	wait := func(label string, ready func() bool) {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if ready() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		status, _ := installed.Query()
		t.Fatalf("timeout waiting for %s: own service status=%+v", label, status)
	}
	var running svc.Status
	wait("SCM Running", func() bool {
		status, e := installed.Query()
		running = status
		return e == nil && status.State == svc.Running
	})
	if running.ProcessId == 0 {
		t.Fatal("running SCM service has no process ID")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, running.ProcessId)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	wait("real worker hello", func() bool { return api.Status().WorkersOnline == 1 })
	doJob := func() {
		body, _ := json.Marshal(protocol.SubmitRequest{Model: "scm:model", Prompt: "exercise native SCM worker", DeadlineMS: 5000})
		req, err := http.NewRequest(http.MethodPost, backend.URL+"/v1/jobs?wait=3s", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer inert-scm-caller-token")
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result protocol.JobStatus
		decodeErr := json.NewDecoder(response.Body).Decode(&result)
		response.Body.Close()
		if decodeErr != nil || response.StatusCode != http.StatusOK || result.State != protocol.StateDone || result.Output != "SCM job completed" {
			t.Fatalf("real SCM job: status=%d result=%+v error=%v", response.StatusCode, result, decodeErr)
		}
	}
	doJob()
	wait("owned long poll", func() bool { return activePoll.Load() > 0 })
	if _, err := installed.Control(svc.Stop); err != nil {
		t.Fatal(err)
	}
	wait("SCM Stopped", func() bool { status, e := installed.Query(); return e == nil && status.State == svc.Stopped })
	status, err := windows.WaitForSingleObject(process, 5000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned worker process remains: status=%d error=%v", status, err)
	}
	wait("worker goodbye", func() bool { return goodbye.Load() > 0 && api.Status().WorkersOnline == 0 })
	wait("owned poll canceled", func() bool { return activePoll.Load() == 0 && canceledPoll.Load() > 0 })
	// A second real start proves native SCM restart, not only initial launch.
	if err := installed.Start(); err != nil {
		t.Fatal(err)
	}
	wait("SCM restarted Running", func() bool { status, e := installed.Query(); return e == nil && status.State == svc.Running })
	wait("restarted worker hello", func() bool { return api.Status().WorkersOnline == 1 })
	doJob()
	if _, err := installed.Control(svc.Stop); err != nil {
		t.Fatal(err)
	}
	wait("restarted SCM Stopped", func() bool { status, e := installed.Query(); return e == nil && status.State == svc.Stopped })
	wait("restarted worker goodbye", func() bool { return goodbye.Load() >= 2 && api.Status().WorkersOnline == 0 })
	if err := installed.Delete(); err != nil {
		t.Fatal(err)
	}
	deleted = true
	if err := installed.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	deadline := time.Now().Add(10 * time.Second)
	for {
		leftover, e := manager.OpenService(name)
		if errors.Is(e, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			break
		}
		if e == nil {
			leftover.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("exact test service registration remains: %v", e)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Log("PASS: unique manual LocalSystem service created -> SCM Running -> real hello/job/result -> stop -> poll canceled/goodbye/process exited -> native restart/hello/second job/stop -> exact service deleted")
}

func protectSystemToken(t *testing.T, path string) {
	t.Helper()
	// LocalSystem worker accepts only its own SID and trusted Administrators.
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, acl, nil)
	if err != nil {
		t.Fatalf("protect scoped LocalSystem test token (requires elevated administrator): %v", err)
	}
}
