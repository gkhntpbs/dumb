//go:build windows

// Package windowse2e exercises real Windows workers against inert loopback fixtures.
package windowse2e

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/pe"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/server"
	"github.com/gkhntpbs/dumb/internal/update"
	"github.com/gkhntpbs/dumb/internal/update/updatetest"
	"golang.org/x/sys/windows"
)

func TestSignedWindowsWorkerLifecycle(t *testing.T) {
	if os.Getenv("QA_WINDOWS_UPDATE") != "1" {
		t.Skip("set QA_WINDOWS_UPDATE=1 to build and run isolated workers")
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
		image, err := pe.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		header, ok := image.OptionalHeader.(*pe.OptionalHeader64)
		if !ok || header.Subsystem != 2 {
			image.Close()
			t.Fatal("worker is not a Windows GUI-subsystem executable")
		}
		image.Close()
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
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
	name := update.ArchiveName("0.2.0", "windows", runtime.GOARCH)
	sum := sha256.Sum256(archive.Bytes())
	checksums := []byte(fmt.Sprintf("%x  %s\n", sum, name))
	signature := signer.Sign(checksums, "isolated Windows e2e")
	var corrupt atomic.Bool
	var listings atomic.Int32
	var rejectHello atomic.Bool
	var releaseURL string
	releases := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/gkhntpbs/dumb/releases":
			if listings.Add(1) == 1 {
				http.Error(w, "fixture startup miss", 503)
				return
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"tag_name": "v0.2.0", "prerelease": false, "assets": []any{
				map[string]string{"name": name, "browser_download_url": releaseURL + "/" + name},
				map[string]string{"name": "checksums.txt", "browser_download_url": releaseURL + "/checksums.txt"},
				map[string]string{"name": "checksums.txt.minisig", "browser_download_url": releaseURL + "/checksums.txt.minisig"},
			}}})
		case "/" + name:
			w.Write(archive.Bytes())
		case "/checksums.txt":
			w.Write(checksums)
		case "/checksums.txt.minisig":
			if corrupt.Load() {
				w.Write(updatetest.NewSigner().Sign(checksums, "wrong key"))
			} else {
				w.Write(signature)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer releases.Close()
	releaseURL = releases.URL
	tokens := auth.NewStaticTokens()
	tokens.Add("windows-e2e", auth.RoleWorker, "inert-local-token")
	api, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.2.0", MaxWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectHello.Load() {
			http.Error(w, "fixture offline", 503)
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer backend.Close()
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"version":"fixture"}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"e2e:model"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollama.Close()
	tokenFile := filepath.Join(temp, "token")
	write(t, tokenFile, []byte("inert-local-token"))
	protect(t, tokenFile)
	exe := filepath.Join(temp, "dumb-worker.exe")
	args := []string{"run", "-headless", "-server", backend.URL, "-token-file", tokenFile, "-models", "e2e:model", "-ollama", ollama.URL, "-update-url", releases.URL}
	start := func(label string) (*exec.Cmd, string, <-chan error) {
		logPath := filepath.Join(temp, label+".log")
		output, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(exe, args...)
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		command.Stdout, command.Stderr = output, output
		if err := command.Start(); err != nil {
			output.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { err := command.Wait(); output.Close(); done <- err }()
		t.Cleanup(func() { _ = command.Process.Kill() })
		return command, logPath, done
	}
	exit4 := func(done <-chan error, logPath string) {
		select {
		case err := <-done:
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 4 {
				t.Fatalf("expected exit 4: %v\n%s", err, read(t, logPath))
			}
		case <-time.After(40 * time.Second):
			t.Fatalf("worker did not exit for restart\n%s", read(t, logPath))
		}
	}
	waitLog := func(path, text string) {
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), text) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("missing %q:\n%s", text, read(t, path))
	}
	write(t, exe, binaries["0.1.0"])
	_, updateLog, done := start("signed-update")
	exit4(done, updateLog)
	waitLog(updateLog, "UPGRADE REQUIRED")
	command, commitLog, done := start("restarted-new")
	waitLog(commitLog, "update: 0.2.0 committed")
	assertNoVisibleWindows(t, command.Process.Pid)
	_ = command.Process.Kill()
	<-done
	if !bytes.Equal(read(t, exe), binaries["0.2.0"]) {
		t.Fatal("committed binary differs")
	}
	noStaging(t, exe)
	t.Log("PASS: signed 0.1.0 -> 426 -> 0.2.0 -> exit 4 -> real process restart -> hello -> commit")

	corrupt.Store(true)
	write(t, exe, binaries["0.1.0"])
	command, badLog, done := start("bad-signature")
	waitLog(badLog, "signature: made with key")
	waitLog(badLog, "staying up")
	assertNoVisibleWindows(t, command.Process.Pid)
	if !bytes.Equal(read(t, exe), binaries["0.1.0"]) {
		t.Fatal("bad signature modified executable")
	}
	noStaging(t, exe)
	select {
	case err := <-done:
		t.Fatalf("bad signature stopped worker: %v", err)
	default:
	}
	_ = command.Process.Kill()
	<-done
	t.Log("PASS: wrong-key signature refused; running worker remains alive and byte-identical")

	corrupt.Store(false)
	_, rollbackInstallLog, rollbackDone := start("rollback-install")
	exit4(rollbackDone, rollbackInstallLog)
	rejectHello.Store(true)
	for attempt := 1; attempt <= 3; attempt++ {
		command, logPath, done := start(fmt.Sprintf("probation-%d", attempt))
		waitLog(logPath, fmt.Sprintf("on probation (start %d", attempt))
		_ = command.Process.Kill()
		<-done
	}
	_, rollbackLog, done := start("rollback-fourth-start")
	exit4(done, rollbackLog)
	waitLog(rollbackLog, "rolled back 0.2.0")
	if !bytes.Equal(read(t, exe), binaries["0.1.0"]) {
		t.Fatal("rollback did not restore original")
	}
	skip := read(t, exe+".skip.json")
	if !bytes.Contains(skip, []byte("0.2.0")) {
		t.Fatal("rollback version not skipped")
	}
	rejectHello.Store(false)
	command, recoveredLog, done := start("restarted-old")
	waitLog(recoveredLog, "staying up")
	if _, err := os.Stat(exe + ".bad"); !os.IsNotExist(err) {
		t.Fatalf("rollback image not removed after old process restart: %v", err)
	} // Minimum-version refusal cannot reapply the skipped release.
	_ = command.Process.Kill()
	<-done
	t.Log("PASS: three unsuccessful real starts -> mapped-exe rollback -> exit 4 -> old worker restart; bad version skipped")
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func noStaging(t *testing.T, exe string) {
	t.Helper()
	for _, s := range []string{".new", ".prev", ".update.json"} {
		if _, e := os.Stat(exe + s); !os.IsNotExist(e) {
			t.Fatalf("staging remains: %s %v", s, e)
		}
	}
}
func protect(t *testing.T, path string) {
	t.Helper()
	token, e := windows.OpenCurrentProcessToken()
	if e != nil {
		t.Fatal(e)
	}
	defer token.Close()
	user, e := token.GetTokenUser()
	if e != nil {
		t.Fatal(e)
	}
	sd, e := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	if e != nil {
		t.Fatal(e)
	}
	acl, _, e := sd.DACL()
	if e != nil {
		t.Fatal(e)
	}
	if e = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
		t.Fatal(e)
	}
}

// Only inspect top-level windows belonging to the scoped worker PID.
func assertNoVisibleWindows(t *testing.T, pid int) {
	t.Helper()
	user32 := windows.NewLazySystemDLL("user32.dll")
	getPID := user32.NewProc("GetWindowThreadProcessId")
	visible := user32.NewProc("IsWindowVisible")
	var count int
	callback := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var windowPID uint32
		getPID.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))
		isVisible, _, _ := visible.Call(hwnd)
		if windowPID == uint32(pid) && isVisible != 0 {
			count++
		}
		return 1
	})
	result, _, err := user32.NewProc("EnumWindows").Call(callback, 0)
	if result == 0 {
		t.Fatalf("inspect owned windows: %v", err)
	}
	if count != 0 {
		t.Fatalf("headless GUI-subsystem worker opened %d visible windows", count)
	}
	t.Log("PASS: Windows GUI subsystem=2, CREATE_NO_WINDOW enabled, visible owned windows=0")
}
