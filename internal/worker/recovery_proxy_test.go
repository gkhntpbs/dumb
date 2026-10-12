package worker

import (
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/recoverylab"
)

func TestRecoveryLabImplicitProxyIsRemembered(t *testing.T) {
	fixture := recoverylab.New(t)
	fixture.SetRunning(true)
	fixture.AddModel("fixture")
	lab := newWorkerRecoveryLab(t, fixture, false, false)
	lab.run(t)
	eventually(t, "implicit existing API ready", func() bool { return lab.server.Status().WorkersOnline == 1 })
	fixture.SetRunning(false)
	time.Sleep(50 * time.Millisecond)
	snapshot := fixture.Snapshot()
	if snapshot.Downloads+snapshot.Starts+snapshot.Stops != 0 {
		t.Fatalf("previously healthy implicit proxy modified: %+v", snapshot)
	}
	if lab.server.Status().WorkersOnline != 0 {
		t.Fatal("unavailable implicit proxy still advertised")
	}
}
