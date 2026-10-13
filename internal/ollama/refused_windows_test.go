//go:build windows

package ollama

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestWindowsRealRefusalMatchesRecoveryClassification(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	client, err := New("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Version(context.Background())
	if !isConnectionRefused(err) {
		t.Fatalf("native refusal not recognized: %v", err)
	}
}

func TestWindowsRefusalDoesNotPermitOccupiedErrors(t *testing.T) {
	for _, err := range []error{windows.WSAECONNRESET, windows.WSAETIMEDOUT, windows.WSAEACCES, context.DeadlineExceeded} {
		if isConnectionRefused(err) {
			t.Fatalf("occupied/unresponsive error classified as absence: %v", err)
		}
	}
	if !isConnectionRefused(syscall.ECONNREFUSED) || !isConnectionRefused(windows.WSAECONNREFUSED) {
		t.Fatal("refusal not recognized")
	}
}
func TestWindowsDefaultEndpointNativeRefusal(t *testing.T) {
	if os.Getenv("QA_WINDOWS_OLLAMA") != "1" {
		t.Skip("explicit opt-in default endpoint probe")
	}
	connection, err := net.Dial("tcp4", "127.0.0.1:11434")
	if err == nil {
		connection.Close()
		t.Skip("default endpoint occupied; preserving existing owner")
	}
	client, err := New(DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Version(context.Background())
	var errno syscall.Errno
	if !isConnectionRefused(err) || !errors.As(err, &errno) || errno != windows.WSAECONNREFUSED {
		t.Fatalf("default endpoint refused errno=%d error=%v", errno, err)
	}
	t.Log("default endpoint ConnectEx refusal recognized as Winsock 10061")
}
