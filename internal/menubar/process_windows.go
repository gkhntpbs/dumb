//go:build windows

package menubar

import (
	"os/exec"
	"syscall"
)

func configureHelper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
