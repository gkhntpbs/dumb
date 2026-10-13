//go:build windows

package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the real Windows executable sharing rules, rather than swapping an
// inert text fixture: the child moves its own mapped image aside and rolls back.
func TestWindowsRunningExecutableSwapRollback(t *testing.T) {
	if path := os.Getenv("DUMB_TEST_RUNNING_UPDATE"); path != "" {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		u := &Updater{Exe: path}
		if err := u.swap(before, "0.1.0", "0.2.0"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path + ".prev"); err != nil {
			t.Fatal(err)
		}
		p := &Probation{Exe: path, From: "0.1.0", To: "0.2.0"}
		if err := p.Rollback(); !errors.Is(err, ErrRestart) {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(statePath(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("probation state retained: %v", err)
		}
		return
	}
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worker.exe")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestWindowsRunningExecutableSwapRollback$")
	cmd.Env = append(os.Environ(), "DUMB_TEST_RUNNING_UPDATE="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("running update: %v\n%s", err, out)
	}
}
