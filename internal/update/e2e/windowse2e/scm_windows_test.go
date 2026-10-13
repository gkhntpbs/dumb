//go:build windows

package windowse2e

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
	"github.com/gkhntpbs/dumb/internal/service"
	"github.com/gkhntpbs/dumb/internal/update"
	"github.com/gkhntpbs/dumb/internal/update/updatetest"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Exercise the production service recovery policy with a real mapped signed
// worker update, not a user-space loop that merely imitates a service manager.
func TestSignedWindowsSCMUpdateAndRecovery(t *testing.T) {
	if os.Getenv("QA_WINDOWS_UPDATE_SCM") != "1" {
		t.Skip("set QA_WINDOWS_UPDATE_SCM=1 for isolated signed update through native SCM recovery")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	signer := updatetest.NewSigner()
	binaries := map[string][]byte{}
	for _, version := range []string{"0.1.0", "0.2.0"} {
		out := filepath.Join(temp, version+".exe")
		cmd := exec.Command("go", "build", "-trimpath", "-o", out, "-ldflags", "-H windowsgui -X main.version="+version+" -X github.com/gkhntpbs/dumb/internal/update.PublicKey="+signer.PublicKey(), "./cmd/dumb-worker")
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", version, err, output)
		}
		binaries[version] = read(t, out)
	}
	var arc bytes.Buffer
	zw := zip.NewWriter(&arc)
	entry, err := zw.Create("dumb-worker.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(binaries["0.2.0"]); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	asset := update.ArchiveName("0.2.0", "windows", runtime.GOARCH)
	sum := sha256.Sum256(arc.Bytes())
	checksums := []byte(fmt.Sprintf("%x  %s\n", sum, asset))
	signature := signer.Sign(checksums, "native SCM acceptance fixture")
	wrongSignature := updatetest.NewSigner().Sign(checksums, "wrong SCM fixture key")
	var corrupt atomic.Bool
	var listings, badSignatures, oldUpgrade, newHello atomic.Int32
	releaseGate := make(chan struct{})
	var releaseURL string
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gkhntpbs/dumb/releases":
			if listings.Add(1) == 1 {
				http.Error(w, "fixture startup miss", 503)
				return
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"tag_name": "v0.2.0", "prerelease": false, "assets": []any{
				map[string]string{"name": asset, "browser_download_url": releaseURL + "/" + asset},
				map[string]string{"name": "checksums.txt", "browser_download_url": releaseURL + "/checksums.txt"},
				map[string]string{"name": "checksums.txt.minisig", "browser_download_url": releaseURL + "/checksums.txt.minisig"},
			}}})
		case "/checksums.txt":
			select {
			case <-releaseGate:
			case <-r.Context().Done():
				return
			}
			w.Write(checksums)
		case "/checksums.txt.minisig":
			if corrupt.Load() {
				badSignatures.Add(1)
				w.Write(wrongSignature)
			} else {
				w.Write(signature)
			}
		case "/" + asset:
			w.Write(arc.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(releases.Close)
	releaseURL = releases.URL
	tokens := auth.NewStaticTokens()
	tokens.Add("scm-update-worker", auth.RoleWorker, "inert-scm-update-token")
	api, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.2.0", MaxWait: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workers/me" && r.Header.Get(protocol.WorkerVersionHeader) == "0.1.0" {
			oldUpgrade.Add(1)
		}
		if r.URL.Path == "/v1/workers/hello" {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "body", 400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			var hello protocol.HelloRequest
			if json.Unmarshal(data, &hello) == nil {

				if hello.WorkerVersion == "0.2.0" {
					newHello.Add(1)
				}
			}
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(backend.Close)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"scm-update-fixture"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"scm-update:model"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ollama.Close)
	tokenFile := filepath.Join(temp, "token")
	write(t, tokenFile, []byte("inert-scm-update-token"))
	protectSCMUpdateToken(t, tokenFile)
	manager, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Disconnect() })
	// Each installation has an independent random name and executable directory.
	install := func(label string) (*mgr.Service, string, string) {
		entropy := make([]byte, 8)
		if _, err := rand.Read(entropy); err != nil {
			t.Fatal(err)
		}
		name := "DUMB-QA-Update-" + hex.EncodeToString(entropy)
		directory := filepath.Join(temp, label)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(directory, "dumb-worker.exe")
		write(t, exe, binaries["0.1.0"])
		args := []string{"run", "-headless", "-server", backend.URL, "-token-file", tokenFile, "-models", "scm-update:model", "-ollama", ollama.URL, "-update-url", releases.URL}
		installed, err := manager.CreateService(name, exe, mgr.Config{ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: mgr.StartManual, ErrorControl: mgr.ErrorNormal, ServiceStartName: "LocalSystem", DisplayName: name, Description: "Temporary signed DUMB SCM update fixture"}, args...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// Disable recovery only on this exact test service before scoped shutdown.
			_ = installed.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.NoAction}}, 86400)
			_, _ = installed.Control(svc.Stop)
			deadline := time.Now().Add(20 * time.Second)
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
				t.Errorf("exact owned service %s did not stop; retained for inspection", name)
				installed.Close()
				return
			}
			if err := installed.Delete(); err != nil {
				t.Errorf("delete own service %s: %v", name, err)
			}
			installed.Close()
			deadline = time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				remaining, e := manager.OpenService(name)
				if errors.Is(e, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
					t.Log("PASS: exact owned SCM update service deleted")
					return
				}
				if e == nil {
					remaining.Close()
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Errorf("exact owned SCM update service %s remains registered", name)
		})
		for _, scArgs := range [][]string{service.WindowsFailureArgs(service.Config{Label: name}), {"failureflag", name, "1"}} {
			if output, err := exec.Command("sc.exe", scArgs...).CombinedOutput(); err != nil {
				t.Fatalf("configure production recovery policy: %v\n%s", err, output)
			}
		}
		actions, err := installed.RecoveryActions()
		if err != nil {
			t.Fatal(err)
		}
		if len(actions) != 3 || actions[0].Type != mgr.ServiceRestart || actions[0].Delay != 0 || actions[1].Type != mgr.ServiceRestart || actions[1].Delay != 5*time.Second || actions[2].Type != mgr.ServiceRestart || actions[2].Delay != 60*time.Second {
			t.Fatalf("production recovery actions differ: %+v", actions)
		}
		enabled, err := installed.RecoveryActionsOnNonCrashFailures()
		if err != nil || !enabled {
			t.Fatalf("non-crash recovery flag=%v error=%v", enabled, err)
		}
		return installed, name, exe
	}
	wait := func(installed *mgr.Service, label string, ready func() bool) {
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if ready() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		status, _ := installed.Query()
		t.Fatalf("timeout %s: own status=%+v old-upgrade-check=%d new-hello=%d", label, status, oldUpgrade.Load(), newHello.Load())
	}
	installed, _, exe := install("signed")
	if err := installed.Start(); err != nil {
		t.Fatal(err)
	}
	var oldPID uint32
	wait(installed, "old SCM worker Running/426", func() bool {
		status, e := installed.Query()
		oldPID = status.ProcessId
		return e == nil && status.State == svc.Running && oldPID != 0 && oldUpgrade.Load() > 0
	})
	oldProcess, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, oldPID)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(oldProcess)
	close(releaseGate)
	var newPID uint32
	wait(installed, "automatic SCM restart/0.2.0 hello/commit", func() bool {
		status, e := installed.Query()
		newPID = status.ProcessId
		_, stateErr := os.Stat(exe + ".update.json")
		return e == nil && status.State == svc.Running && newPID != 0 && newPID != oldPID && newHello.Load() > 0 && os.IsNotExist(stateErr) && bytes.Equal(read(t, exe), binaries["0.2.0"])
	})
	var exitCode uint32
	if err := windows.GetExitCodeProcess(oldProcess, &exitCode); err != nil || exitCode != 4 {
		t.Fatalf("old mapped worker did not exit 4: code=%d error=%v", exitCode, err)
	}
	noStaging(t, exe)
	assertNoVisibleWindows(t, int(newPID))
	t.Log("PASS: production SCM recovery policy; signed old mapped worker exit=4 -> automatic native restart with changed PID -> actual 0.2.0 hello -> commit")

	// Crash only the process handle obtained from this exact created service.
	crashed, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, newPID)
	if err != nil {
		t.Fatal(err)
	}
	beforeHello := newHello.Load()
	if err := windows.TerminateProcess(crashed, 137); err != nil {
		windows.CloseHandle(crashed)
		t.Fatal(err)
	}
	windows.CloseHandle(crashed)
	wait(installed, "automatic SCM crash recovery", func() bool {
		status, e := installed.Query()
		return e == nil && status.State == svc.Running && status.ProcessId != 0 && status.ProcessId != newPID && newHello.Load() > beforeHello
	})
	t.Log("PASS: terminate exact owned service PID -> production second failure action -> automatic native restart and 0.2.0 hello")
	if _, err := installed.Control(svc.Stop); err != nil {
		t.Fatal(err)
	}
	wait(installed, "signed SCM service stopped", func() bool { status, e := installed.Query(); return e == nil && status.State == svc.Stopped })

	stopped, err := installed.Query()
	if err != nil || stopped.Win32ExitCode != 0 || stopped.ServiceSpecificExitCode != 0 {
		t.Fatalf("normal SCM Stop reported failure: %+v %v", stopped, err)
	}
	t.Log("PASS: explicit SCM Stop exits cleanly with zero Win32/service-specific codes; recovery is not triggered")

	corrupt.Store(true)
	beforeBad := badSignatures.Load()
	rejected, _, badExe := install("bad-signature")
	if err := rejected.Start(); err != nil {
		t.Fatal(err)
	}
	var badPID uint32
	wait(rejected, "wrong signature refused by running SCM worker", func() bool {
		status, e := rejected.Query()
		badPID = status.ProcessId
		return e == nil && status.State == svc.Running && badPID != 0 && badSignatures.Load() >= beforeBad+2
	})
	time.Sleep(500 * time.Millisecond)
	status, err := rejected.Query()
	if err != nil || status.State != svc.Running || status.ProcessId != badPID {
		t.Fatalf("bad signature worker exited/restarted: %+v %v", status, err)
	}
	if !bytes.Equal(read(t, badExe), binaries["0.1.0"]) {
		t.Fatal("wrong signature modified mapped SCM worker")
	}
	noStaging(t, badExe)
	assertNoVisibleWindows(t, int(badPID))
	t.Log("PASS: wrong-key signature rejected at startup and minimum-version retry; native SCM worker stays on same PID and original byte-identical executable")
	stopped, err = installed.Query()
	if err != nil || stopped.State != svc.Stopped {
		t.Fatalf("normal Stop unexpectedly restarted signed service: %+v %v", stopped, err)
	}
}

func protectSCMUpdateToken(t *testing.T, path string) {
	t.Helper()
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
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}
