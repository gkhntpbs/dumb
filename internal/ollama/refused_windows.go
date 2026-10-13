//go:build windows

package ollama

import (
	"errors"
	"golang.org/x/sys/windows"
	"syscall"
)

// syscall.ECONNREFUSED is a synthetic compatibility errno on Windows. Real
// ConnectEx refusal is Winsock 10061; resets, timeouts and access errors do not
// establish absence and must never authorize install/start over an endpoint.
func isConnectionRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, syscall.ECONNREFUSED)
}
