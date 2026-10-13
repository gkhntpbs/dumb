//go:build !windows

package ollama

import "os/exec"

func attachOwnedProcess(*exec.Cmd) (func(), error) { return func() {}, nil }
