//go:build windows

package ollama

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsOwnedProcessTree(t *testing.T) {
	if len(os.Args) > 1 && os.Getenv("DUMB_TEST_TREE_ROLE") != "" {
		role := os.Getenv("DUMB_TEST_TREE_ROLE")
		if role == "leaf" {
			time.Sleep(time.Minute)
			return
		}
		marker := os.Getenv("DUMB_TEST_TREE_MARKER")
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		exe, _ := os.Executable()
		child := exec.Command(exe, "-test.run=^TestWindowsOwnedProcessTree$")
		child.Env = append(os.Environ(), "DUMB_TEST_TREE_ROLE=leaf")
		hideConsole(child)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker+".pid", []byte(fmt.Sprint(child.Process.Pid)), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "start-child")
	// A sibling outside our unnamed job must survive job cleanup.
	sibling := exec.Command(exe, "-test.run=^TestWindowsOwnedProcessTree$")
	sibling.Env = append(os.Environ(), "DUMB_TEST_TREE_ROLE=leaf")
	hideConsole(sibling)
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sibling.Process.Kill(); _ = sibling.Wait() }()
	siblingHandle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(sibling.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(siblingHandle)
	t.Setenv("DUMB_TEST_TREE_ROLE", "parent")
	t.Setenv("DUMB_TEST_TREE_MARKER", marker)
	owned, err := (ExecRunner{}).StartManaged(exe, "-test.run=^TestWindowsOwnedProcessTree$")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer owned.Stop(ctx)
	if err := os.WriteFile(marker, []byte("start"), 0600); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker + ".pid")
		if err == nil {
			pid, err = strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("owned descendant did not start")
	}
	descendant, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(descendant)
	if err := owned.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := windows.WaitForSingleObject(descendant, 5000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant survived owned parent cleanup: status=%d error=%v", status, err)
	}
	status, err = windows.WaitForSingleObject(siblingHandle, 0)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("unrelated sibling was affected: status=%d error=%v", status, err)
	}
}
