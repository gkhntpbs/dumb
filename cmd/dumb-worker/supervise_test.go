package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/update"
	"github.com/gkhntpbs/dumb/internal/worker"
)

// fakeRunner: run is called for every Run; Drain makes a blocked Run return nil.
type fakeRunner struct {
	run      func(ctx context.Context, drained <-chan struct{}) error
	drained  chan struct{}
	drainOne sync.Once
}

func (f *fakeRunner) Run(ctx context.Context) error { return f.run(ctx, f.drained) }
func (f *fakeRunner) Drain()                        { f.drainOne.Do(func() { close(f.drained) }) }

type fakeUpdater struct {
	calls  atomic.Int32
	mins   chan string
	result func(call int32) error // nil error = applied
}

func (f *fakeUpdater) Update(ctx context.Context, min string) (protocol.ReleaseVersion, error) {
	n := f.calls.Add(1)
	select {
	case f.mins <- min:
	default:
	}
	if err := f.result(n); err != nil {
		return protocol.ReleaseVersion{}, err
	}
	return protocol.ParseReleaseVersion("0.2.0")
}

func newSupervisor(run func(ctx context.Context, onHello func(), drained <-chan struct{}) error, upd updater) (*supervisor, *atomic.Int32) {
	var runs atomic.Int32
	s := &supervisor{
		version: "0.1.0", upd: upd, log: log.New(io.Discard, "", 0),
		backoff: time.Millisecond, maxBack: 4 * time.Millisecond,
		newWorker: func(onHello func()) (runner, error) {
			runs.Add(1)
			f := &fakeRunner{drained: make(chan struct{})}
			f.run = func(ctx context.Context, d <-chan struct{}) error { return run(ctx, onHello, d) }
			return f, nil
		},
	}
	return s, &runs
}

func upgradeRequired(ctx context.Context, _ func(), _ <-chan struct{}) error {
	return &worker.ErrUpgradeRequired{MinVersion: "0.2.0"}
}

// An outdated worker with no usable update must not exit (a service manager
// would restart it in a tight loop); it backs off and checks again.
func TestUpgradeRequiredWithoutUpdateStaysUp(t *testing.T) {
	for name, upd := range map[string]updater{
		"no release":       &fakeUpdater{mins: make(chan string, 100), result: func(int32) error { return update.ErrNoUpdate }},
		"updates disabled": nil,
	} {
		t.Run(name, func(t *testing.T) {
			s, runs := newSupervisor(upgradeRequired, upd)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan int, 1)
			go func() { done <- s.run(ctx) }()
			deadline := time.After(5 * time.Second)
			for runs.Load() < 5 {
				select {
				case code := <-done:
					t.Fatalf("exited with %d after %d runs", code, runs.Load())
				case <-deadline:
					t.Fatal("no retries")
				case <-time.After(time.Millisecond):
				}
			}
			cancel()
			if code := <-done; code != exitOK {
				t.Fatalf("exit %d after cancel, want %d", code, exitOK)
			}
			if f, ok := upd.(*fakeUpdater); ok {
				if <-f.mins != "" { // startup check has no minimum
					t.Error("startup check passed a minimum")
				}
				if m := <-f.mins; m != "0.2.0" {
					t.Errorf("426 check min = %q, want 0.2.0", m)
				}
			}
		})
	}
}

func TestUpgradeRequiredAppliesUpdate(t *testing.T) {
	upd := &fakeUpdater{mins: make(chan string, 10), result: func(n int32) error {
		if n == 1 {
			return update.ErrNoUpdate // startup
		}
		return nil
	}}
	s, _ := newSupervisor(upgradeRequired, upd)
	if code := s.run(context.Background()); code != exitRestart {
		t.Fatalf("exit %d, want %d", code, exitRestart)
	}
}

func TestStartupUpdateRestarts(t *testing.T) {
	upd := &fakeUpdater{result: func(int32) error { return nil }}
	s, runs := newSupervisor(upgradeRequired, upd)
	if code := s.run(context.Background()); code != exitRestart || runs.Load() != 0 {
		t.Fatalf("exit %d after %d runs, want %d before any run", code, runs.Load(), exitRestart)
	}
}

// A periodic update drains the worker (jobs finish) before restarting.
func TestPeriodicUpdateDrainsFirst(t *testing.T) {
	upd := &fakeUpdater{result: func(n int32) error {
		if n == 1 {
			return update.ErrNoUpdate
		}
		return nil
	}}
	var drained atomic.Bool
	s, _ := newSupervisor(func(ctx context.Context, _ func(), d <-chan struct{}) error {
		select {
		case <-d:
			drained.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, upd)
	s.every, s.jitter = time.Millisecond, time.Millisecond
	if code := s.run(context.Background()); code != exitRestart || !drained.Load() {
		t.Fatalf("exit %d, drained %v; want %d after a drain", code, drained.Load(), exitRestart)
	}
}

func probationFiles(t *testing.T) (exe string) {
	t.Helper()
	exe = filepath.Join(t.TempDir(), "dumb-worker")
	os.WriteFile(exe, []byte("new"), 0o755)
	os.WriteFile(exe+".prev", []byte("old"), 0o755)
	return exe
}

func TestProbationRollsBackWithoutHello(t *testing.T) {
	exe := probationFiles(t)
	s, _ := newSupervisor(func(ctx context.Context, _ func(), _ <-chan struct{}) error {
		<-ctx.Done() // the server is never reached
		return ctx.Err()
	}, nil)
	s.probation = &update.Probation{Exe: exe, From: "0.1.0", To: "0.2.0", Window: 20 * time.Millisecond}
	if code := s.run(context.Background()); code != exitRestart {
		t.Fatalf("exit %d, want %d", code, exitRestart)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("exe = %q after rollback, want old", b)
	}
}

func TestProbationCommitsOnHello(t *testing.T) {
	exe := probationFiles(t)
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := newSupervisor(func(ctx context.Context, onHello func(), _ <-chan struct{}) error {
		onHello()
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	s.probation = &update.Probation{Exe: exe, From: "0.1.0", To: "0.2.0", Window: time.Minute}
	done := make(chan int, 1)
	go func() { done <- s.run(ctx) }()
	for i := 0; ; i++ {
		if _, err := os.Stat(exe + ".prev"); os.IsNotExist(err) {
			break
		}
		if i > 500 {
			t.Fatal(".prev not removed after hello")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != exitOK {
		t.Fatalf("exit %d, want %d", code, exitOK)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe = %q, want new", b)
	}
}
