package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/update"
)

// skippingUpdater is a fakeUpdater that also reports rolled-back versions.
type skippingUpdater struct {
	*fakeUpdater
	skip map[string]bool
}

func (s skippingUpdater) Skipped(v string) bool { return s.skip[v] }

func runWithRecommendations(t *testing.T, upd updater, versions []string, wait time.Duration) (code int, drained bool) {
	t.Helper()
	var s *supervisor
	var drainedFlag atomic.Bool
	s, _ = newSupervisor(func(ctx context.Context, _ func(), d <-chan struct{}) error {
		for _, v := range versions {
			s.recommended(v)
			time.Sleep(5 * time.Millisecond)
		}
		select {
		case <-d:
			drainedFlag.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, upd)
	s.every = 0 // no periodic checks: only the recommendation can trigger one
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	code = s.run(ctx)
	return code, drainedFlag.Load()
}

func noUpdateAtStartup(then error) func(int32) error {
	return func(n int32) error {
		if n == 1 {
			return update.ErrNoUpdate // the startup check
		}
		return then
	}
}

// A newer recommendation triggers one immediate signed-update check, which
// drains the worker before the restart.
func TestRecommendedNewerTriggersUpdateOnce(t *testing.T) {
	upd := &fakeUpdater{mins: make(chan string, 10), result: noUpdateAtStartup(nil)}
	code, drained := runWithRecommendations(t, upd, []string{"0.2.0", "0.2.0", "0.2.0"}, 5*time.Second)
	if code != exitRestart || !drained {
		t.Fatalf("exit %d, drained %v; want %d after a drain", code, drained, exitRestart)
	}
	if n := upd.calls.Load(); n != 2 {
		t.Fatalf("update calls %d, want 2 (startup + one recommendation)", n)
	}
	<-upd.mins
	if m := <-upd.mins; m != "" {
		t.Errorf("recommended check passed min %q; the floor is only for 426", m)
	}
}

// Repeated hellos with the same recommendation do not hammer the release
// source when the update is not available yet.
func TestRecommendedDebounced(t *testing.T) {
	upd := &fakeUpdater{mins: make(chan string, 10), result: noUpdateAtStartup(update.ErrNoUpdate)}
	code, _ := runWithRecommendations(t, upd, []string{"0.2.0", "0.2.0", "0.2.0", "0.2.0"}, 300*time.Millisecond)
	if code != exitOK {
		t.Fatalf("exit %d, want %d", code, exitOK)
	}
	if n := upd.calls.Load(); n != 2 {
		t.Fatalf("update calls %d, want 2 (startup + one debounced recommendation)", n)
	}
}

func TestRecommendedSameOrOlderIgnored(t *testing.T) {
	upd := &fakeUpdater{mins: make(chan string, 10), result: noUpdateAtStartup(nil)}
	code, _ := runWithRecommendations(t, upd, []string{"0.1.0", "0.0.9", "0.1.0-beta.1", "garbage"}, 300*time.Millisecond)
	if code != exitOK || upd.calls.Load() != 1 {
		t.Fatalf("exit %d, %d update calls; want %d and only the startup check", code, upd.calls.Load(), exitOK)
	}
}

// A version this worker rolled back is never retried because of a recommendation.
func TestRecommendedSkippedVersionNotRetried(t *testing.T) {
	upd := skippingUpdater{&fakeUpdater{mins: make(chan string, 10), result: noUpdateAtStartup(nil)}, map[string]bool{"0.2.0": true}}
	code, _ := runWithRecommendations(t, upd, []string{"0.2.0", "v0.2.0"}, 300*time.Millisecond)
	if code != exitOK || upd.calls.Load() != 1 {
		t.Fatalf("exit %d, %d update calls; want %d and only the startup check", code, upd.calls.Load(), exitOK)
	}
}

// A different, newer recommendation is followed even within the cooldown.
func TestRecommendedNewVersionNotDebounced(t *testing.T) {
	upd := &fakeUpdater{mins: make(chan string, 10), result: func(n int32) error {
		if n < 3 {
			return update.ErrNoUpdate
		}
		return nil
	}}
	code, drained := runWithRecommendations(t, upd, []string{"0.2.0", "0.2.0", "0.3.0"}, 5*time.Second)
	if code != exitRestart || !drained || upd.calls.Load() != 3 {
		t.Fatalf("exit %d, drained %v, calls %d; want restart after 3 checks", code, drained, upd.calls.Load())
	}
}

func TestRecommendedIgnoredWithUpdatesDisabled(t *testing.T) {
	code, _ := runWithRecommendations(t, nil, []string{"9.9.9"}, 200*time.Millisecond)
	if code != exitOK {
		t.Fatalf("exit %d, want %d", code, exitOK)
	}
}
