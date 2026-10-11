//go:build windows

package ollama

import (
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// detach puts the child in its own process group so Ctrl-C on the worker does not stop it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
