package main

import (
	"context"
	"github.com/gkhntpbs/dumb/internal/update"
	"github.com/gkhntpbs/dumb/internal/worker"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestMenuPauseDrainsThenResumeStartsOnce(t *testing.T) {
	controls := make(chan string, 8)
	drained, release, paused := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var starts atomic.Int32
	s := &supervisor{controls: controls, log: log.New(io.Discard, "", 0), status: func(state, category string) {
		if state == "paused" {
			close(paused)
		}
	}}
	s.newWorker = func(func()) (runner, error) {
		n := starts.Add(1)
		return &fakeRunner{drained: make(chan struct{}), run: func(ctx context.Context, d <-chan struct{}) error {
			if n == 1 {
				<-d
				close(drained)
				<-release
				return nil
			}
			select {
			case <-d:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- s.run(ctx) }()
	controls <- "pause"
	select {
	case <-drained:
	case <-ctx.Done():
		t.Fatal("pause did not drain")
	}
	controls <- "resume"
	time.Sleep(20 * time.Millisecond)
	if starts.Load() != 1 {
		t.Fatal("resume overlapped active worker")
	}
	close(release)
	select {
	case <-paused:
	case <-ctx.Done():
		t.Fatal("paused state missing")
	}
	controls <- "resume"
	limit := time.Now().Add(time.Second)
	for starts.Load() != 2 && time.Now().Before(limit) {
		time.Sleep(time.Millisecond)
	}
	if starts.Load() != 2 {
		t.Fatal("resume missing")
	}
	controls <- "resume"
	controls <- "quit"
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("quit failed")
	}
	if starts.Load() != 2 {
		t.Fatal("duplicate worker")
	}
}

func TestMenuDeathStopsClaimsWithoutRestartLoop(t *testing.T) {
	failed := make(chan struct{})
	var starts atomic.Int32
	drained, started := make(chan struct{}), make(chan struct{})
	s := &supervisor{uiFailed: failed, log: log.New(io.Discard, "", 0)}
	s.newWorker = func(func()) (runner, error) {
		starts.Add(1)
		close(started)
		return &fakeRunner{drained: make(chan struct{}), run: func(ctx context.Context, d <-chan struct{}) error { <-d; close(drained); return nil }}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan int, 1)
	go func() { result <- s.run(ctx) }()
	<-started
	close(failed)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("helper death did not drain")
	}
	time.Sleep(20 * time.Millisecond)
	if starts.Load() != 1 {
		t.Fatal("helper death restarted worker")
	}
	select {
	case <-result:
		t.Fatal("helper death exited into service restart loop")
	default:
	}
	cancel()
	if code := <-result; code != exitOK {
		t.Fatal(code)
	}
}

func TestMenuQuitSupersedesPauseDuringDrain(t *testing.T) {
	controls := make(chan string, 8)
	drained, release := make(chan struct{}), make(chan struct{})
	s := &supervisor{controls: controls, log: log.New(io.Discard, "", 0)}
	s.newWorker = func(func()) (runner, error) {
		return &fakeRunner{drained: make(chan struct{}), run: func(ctx context.Context, d <-chan struct{}) error { <-d; close(drained); <-release; return nil }}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- s.run(ctx) }()
	controls <- "pause"
	<-drained
	controls <- "quit"
	time.Sleep(10 * time.Millisecond)
	close(release)
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("quit was discarded during drain")
	}
}

func TestMenuAlreadyDeadHelperNeverStartsWorker(t *testing.T) {
	failed := make(chan struct{})
	close(failed)
	var starts atomic.Int32
	s := &supervisor{uiFailed: failed, log: log.New(io.Discard, "", 0)}
	s.newWorker = func(func()) (runner, error) {
		starts.Add(1)
		return &fakeRunner{drained: make(chan struct{}), run: func(ctx context.Context, d <-chan struct{}) error { <-d; return nil }}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.run(ctx) != exitOK {
		t.Fatal("unexpected exit")
	}
	if starts.Load() != 0 {
		t.Fatal("worker started without menu control")
	}
}

func TestMenuStateCountsAndPriority(t *testing.T) {
	v := &menuView{mode: "starting", category: "none"}
	if status := v.status(); status.State != "recovering" {
		t.Fatal(status)
	}
	v.snapshot.Ready = true
	v.snapshot.Active = 2
	if status := v.status(); status.State != "serving" || status.Active != 2 {
		t.Fatal(status)
	}
	v.mode, v.category, v.updating = "draining", "none", true
	if status := v.status(); status.State != "draining" {
		t.Fatal(status)
	}
	v.mode, v.category = "error", "upgrade"
	if status := v.status(); status.State != "error" || status.Category != "upgrade" {
		t.Fatal(status)
	}
	v.mode, v.category, v.updating = "starting", "none", false
	v.snapshot.Active = 0
	if status := v.status(); status.State != "ready" || status.Category != "none" {
		t.Fatal(status)
	}
}

func TestMenuFatalCredentialKeepsControlsAvailable(t *testing.T) {
	controls := make(chan string, 2)
	entered := make(chan struct{})
	s := &supervisor{controls: controls, log: log.New(io.Discard, "", 0), status: func(state, category string) {
		if state == "error" && category == "credentials" {
			close(entered)
		}
	}}
	s.newWorker = func(func()) (runner, error) {
		return &fakeRunner{drained: make(chan struct{}), run: func(context.Context, <-chan struct{}) error { return worker.ErrUnauthorized }}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- s.run(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("credential state missing")
	}
	select {
	case <-result:
		t.Fatal("fatal error exited into relaunch loop")
	default:
	}
	controls <- "quit"
	if code := <-result; code != exitOK {
		t.Fatal(code)
	}
}

func TestMenuHelperLocationAndHeadlessOptIn(t *testing.T) {
	c, _, err := parseFlags("run", []string{"-headless"}, io.Discard)
	if err != nil || !c.headless {
		t.Fatalf("headless flag: %+v %v", c, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if _, err := menuHelper("/tmp/dumb-worker"); err == nil {
		t.Fatal("unbundled CLI silently accepted")
	}
	exe := filepath.Join(t.TempDir(), "Worker.app", "Contents", "MacOS", "dumb-worker")
	if _, err := menuHelper(exe); err == nil {
		t.Fatal("missing helper accepted")
	}
	helper := filepath.Join(filepath.Dir(exe), "..", "Resources", "DUMBMenu.app", "Contents", "MacOS", "dumb-menu")
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := menuHelper(exe); err == nil {
		t.Fatal("nonexecutable helper accepted")
	}
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := menuHelper(exe); err != nil {
		t.Fatal(err)
	}
}

func TestMenuQuitDuringUpgradeBackoff(t *testing.T) {
	controls, entered := make(chan string, 1), make(chan struct{})
	s := &supervisor{controls: controls, backoff: time.Hour, maxBack: time.Hour, log: log.New(io.Discard, "", 0), status: func(state, category string) {
		if state == "error" && category == "upgrade" {
			close(entered)
		}
	}}
	s.newWorker = func(func()) (runner, error) {
		return &fakeRunner{drained: make(chan struct{}), run: func(context.Context, <-chan struct{}) error { return &worker.ErrUpgradeRequired{MinVersion: "1.0.0"} }}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- s.run(ctx) }()
	<-entered
	controls <- "quit"
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatal(code)
		}
	case <-ctx.Done():
		t.Fatal("quit ignored while waiting for upgrade")
	}
}

func TestMenuUpgradeRetryClearsErrorState(t *testing.T) {
	controls := make(chan string, 1)
	resumed := make(chan struct{})
	var starts atomic.Int32
	s := &supervisor{controls: controls, backoff: time.Millisecond, maxBack: time.Millisecond, log: log.New(io.Discard, "", 0)}
	var states []string
	s.status = func(state, category string) { states = append(states, state) }
	s.newWorker = func(func()) (runner, error) {
		n := starts.Add(1)
		return &fakeRunner{drained: make(chan struct{}), run: func(ctx context.Context, d <-chan struct{}) error {
			if n == 1 {
				return &worker.ErrUpgradeRequired{MinVersion: "1.0.0"}
			}
			close(resumed)
			<-d
			return nil
		}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan int, 1)
	go func() { result <- s.run(ctx) }()
	<-resumed
	controls <- "quit"
	<-result
	if len(states) < 3 || states[0] != "starting" || states[1] != "error" || states[2] != "starting" {
		t.Fatal(states)
	}
}

func TestMenuUnavailablePreservesRollbackRestart(t *testing.T) {
	failed := make(chan struct{})
	close(failed)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(update.ErrRestart)
	s := &supervisor{uiFailed: failed, log: log.New(io.Discard, "", 0)}
	if code := s.run(ctx); code != exitRestart {
		t.Fatalf("rollback restart lost: %d", code)
	}
}

func TestMenuPreviewDuration(t *testing.T) {
	for _, test := range []struct {
		raw      string
		duration time.Duration
		valid    bool
	}{{"", 180 * time.Second, true}, {"28800", 8 * time.Hour, true}, {"1", time.Second, true}, {"0", 0, false}, {"-1", 0, false}, {"28801", 0, false}, {"invalid", 0, false}} {
		duration, err := previewDuration(test.raw)
		if (err == nil) != test.valid || duration != test.duration {
			t.Fatalf("duration %q: %v %v", test.raw, duration, err)
		}
	}
}

func TestWindowsHelperMustBeRegularSibling(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "dumb-worker.exe")
	if _, err := windowsMenuHelper(exe); err == nil {
		t.Fatal("missing tray accepted")
	}
	helper := filepath.Join(filepath.Dir(exe), "dumb-tray.exe")
	if err := os.Mkdir(helper, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := windowsMenuHelper(exe); err == nil {
		t.Fatal("directory accepted")
	}
	os.Remove(helper)
	if err := os.WriteFile(helper, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := windowsMenuHelper(exe); err != nil || got != helper {
		t.Fatalf("%s %v", got, err)
	}
}
