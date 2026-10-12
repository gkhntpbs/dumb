package ollama

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRecoveryStartsMacCLIWithoutGUI(t *testing.T) {
	down := recoveryOfflineClient(t)
	app := "/Applications/Ollama.app"
	binary := filepath.Join(app, "Contents", "Resources", "ollama")
	system, runner, _ := fakeSystem("darwin", []string{app, binary}, down)
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.runs) != 0 || len(runner.starts) != 1 || runner.starts[0] != binary+" serve" {
		t.Fatalf("interactive or missing CLI start: runs=%v starts=%v", runner.runs, runner.starts)
	}
}

type recoveryOfflineTransport struct{}

func (recoveryOfflineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}
func recoveryOfflineClient(t *testing.T) *Client {
	t.Helper()
	client, err := New(DefaultURL)
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = recoveryOfflineTransport{}
	return client
}
