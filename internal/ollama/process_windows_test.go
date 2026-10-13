//go:build windows

package ollama

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestWindowsOwnedChild(t *testing.T) {
	if os.Getenv("DUMB_TEST_OWNED_CHILD") == "1" {
		time.Sleep(time.Minute)
		return
	}
	t.Setenv("DUMB_TEST_OWNED_CHILD", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := (ExecRunner{}).StartManaged(exe, "-test.run=^TestWindowsOwnedChild$")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer p.Stop(ctx)
	if !p.Running() {
		t.Fatal("child did not start")
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Running() {
		t.Fatal("owned child remains alive after Stop")
	}
}

func TestWindowsChildrenHaveNoConsole(t *testing.T) {
	cmd := exec.Command("unused.exe")
	detach(cmd)
	if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("child can open a console")
	}
}
