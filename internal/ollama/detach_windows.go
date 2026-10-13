//go:build windows

package ollama

import (
	"os"
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// detach puts the child in its own process group so Ctrl-C on the worker does not stop it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNewProcessGroup | 0x08000000}
}

func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// Windows cannot deliver os.Interrupt to a console-free child. Kill only the owned PID.
func interruptOwned(process *os.Process) error { return process.Kill() }
