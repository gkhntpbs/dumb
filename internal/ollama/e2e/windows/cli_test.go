//go:build windows

package windowse2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
	"golang.org/x/sys/windows"
)

// This installed-case test runs the actual GUI-subsystem worker's run entry
// point. Cancellation is a forced worker exit, proving its noninherited job
// handle kills only owned Ollama/model descendants. SCM tests cover graceful stop.
func TestActualWindowsWorkerWithInstalledOllama(t *testing.T) {
	if os.Getenv("QA_WINDOWS_OLLAMA_RUN") != "1" {
		t.Skip("QA_WINDOWS_OLLAMA_RUN=1 authorizes an installed-case real worker run")
	}
	root := os.Getenv("DUMB_OLLAMA_DATA_DIR")
	if root == "" || !filepath.IsAbs(root) {
		t.Fatal("explicit absolute owned data root required")
	}
	if _, err := os.Stat(filepath.Join(root, "Ollama", ".dumb-owned.json")); err != nil {
		t.Fatal("a marked official portable installation is required", err)
	}
	connection, err := net.DialTimeout("tcp4", "127.0.0.1:11434", time.Second)
	if err == nil {
		connection.Close()
		t.Fatal("default endpoint is occupied; no worker started")
	}
	for key, value := range map[string]string{"OLLAMA_NUM_PARALLEL": "1", "OLLAMA_MAX_LOADED_MODELS": "1", "OLLAMA_CONTEXT_LENGTH": "512", "OLLAMA_MODELS": filepath.Join(root, "models"), "CUDA_VISIBLE_DEVICES": "-1", "ROCR_VISIBLE_DEVICES": "-1", "GGML_VK_VISIBLE_DEVICES": "-1", "OLLAMA_VULKAN": "0", "OLLAMA_NO_CLOUD": "1"} {
		t.Setenv(key, value)
	}
	t.Setenv("DUMB_OLLAMA", "")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	core, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "acceptance-worker.exe")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-H windowsgui", "-o", binary, "./cmd/dumb-worker")
	build.Dir = core
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("worker build %v %s", err, output)
	}
	workerToken, callerToken := credential(), credential()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(workerToken), 0600); err != nil {
		t.Fatal(err)
	}
	protectToken(t, tokenFile)
	tokens := auth.NewStaticTokens()
	tokens.Add("real-worker", auth.RoleWorker, workerToken)
	tokens.Add("real-caller", auth.RoleCaller, callerToken)
	service, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.0.0", MaxWait: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(service)
	defer endpoint.Close()
	command := exec.CommandContext(ctx, binary, "run", "-headless", "-server", endpoint.URL, "-token-file", tokenFile, "-models", "smollm2:135m")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	var descendants []windows.Handle
	defer func() {
		_ = command.Process.Kill()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Error("owned worker exit timed out")
		}
		for _, handle := range descendants {
			windows.CloseHandle(handle)
		}
	}()
	await(t, 30*time.Second, "actual worker announced model", func() bool { return service.Status().WorkersOnline == 1 })
	ollamaPID := findChildPID(t, uint32(command.Process.Pid), "ollama.exe")
	limitCPU(t, int(ollamaPID))
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, ollamaPID)
	if err != nil {
		t.Fatal(err)
	}
	descendants = append(descendants, handle)
	body, _ := json.Marshal(protocol.SubmitRequest{Model: "smollm2:135m", Prompt: "Reply with one short word.", NumPredict: 8, DeadlineMS: 60000})
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
		t.Fatalf("actual worker generation failed code=%d state=%s error=%s", response.StatusCode, result.State, result.Error)
	}
	for _, modelPID := range childProcessIDs(t, ollamaPID) {
		modelHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, modelPID)
		if err != nil {
			t.Fatal(err)
		}
		descendants = append(descendants, modelHandle)
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("forced worker exit timed out")
	}
	// The channel has been drained; avoid waiting again in cleanup.
	exited <- nil
	for _, descendant := range descendants {
		state, err := windows.WaitForSingleObject(descendant, 10000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatalf("owned descendant survived state=%d error=%v", state, err)
		}
	}
	await(t, 10*time.Second, "default API closed after forced worker exit", func() bool {
		connection, err := net.DialTimeout("tcp4", "127.0.0.1:11434", time.Second)
		if err == nil {
			connection.Close()
		}
		return err != nil
	})
	t.Log("actual GUI-subsystem headless worker served a real tiny-model DUMB job; forced worker exit removed owned Ollama and all observed model descendants")
}
func protectToken(t *testing.T, path string) {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}
func findChildPID(t *testing.T, parent uint32, name string) uint32 {
	t.Helper()
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		if entry.ParentProcessID == parent && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), name) {
			return entry.ProcessID
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	t.Fatalf("owned child %s not found", name)
	return 0
}

func childProcessIDs(t *testing.T, parent uint32) []uint32 {
	t.Helper()
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var children []uint32
	err = windows.Process32First(snapshot, &entry)
	for err == nil {
		if entry.ParentProcessID == parent {
			children = append(children, entry.ProcessID)
		}
		err = windows.Process32Next(snapshot, &entry)
	}
	return children
}
