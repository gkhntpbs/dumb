//go:build !windows

package ollama

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own process group so Ctrl-C on the worker does not stop it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
