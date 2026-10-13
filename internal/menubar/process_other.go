//go:build !windows

package menubar

import "os/exec"

func configureHelper(cmd *exec.Cmd) {}
