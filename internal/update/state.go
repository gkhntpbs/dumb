package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"sync"
	"time"
)

// ErrRestart asks the caller to exit with the restart code so the service
// manager starts the binary now in place (after an update or a rollback).
var ErrRestart = errors.New("update: restart required")

// Probation defaults.
const (
	DefaultWindow      = 2 * time.Minute // a new version must say hello within this
	DefaultMaxAttempts = 3               // starts of a new version before it is declared crash-looping
)

// state is <exe>.update.json, present while an update is on probation.
type state struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Attempts  int       `json:"attempts"`
	StartedAt time.Time `json:"started_at"`
}

func statePath(exe string) string { return exe + ".update.json" }
func skipPath(exe string) string  { return exe + ".skip.json" }

func writeJSONFile(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := name + ".tmp"
	if err := writeSynced(tmp, append(b, '\n'), 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return rename(tmp, name)
}

func writeState(exe string, st state) error { return writeJSONFile(statePath(exe), st) }

func readState(exe string) (state, bool, error) {
	var st state
	b, err := os.ReadFile(statePath(exe))
	if errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, true, fmt.Errorf("update: %s: %w", statePath(exe), err)
	}
	return st, true, nil
}

// Skipped reports whether version was rolled back on this machine; such a
// version is never applied again, whoever recommends it.
func (u *Updater) Skipped(version string) bool {
	return slices.Contains(readSkip(u.Exe), version)
}

// readSkip returns the versions that were rolled back and must not be retried.
func readSkip(exe string) []string {
	var skip []string
	if b, err := os.ReadFile(skipPath(exe)); err == nil {
		json.Unmarshal(b, &skip)
	}
	return skip
}

func addSkip(exe, version string) error {
	skip := readSkip(exe)
	if slices.Contains(skip, version) {
		return nil
	}
	return writeJSONFile(skipPath(exe), append(skip, version))
}

// Probation watches a freshly applied update until its first successful hello.
type Probation struct {
	Exe      string
	From, To string
	Attempt  int
	Window   time.Duration
	Logger   *log.Logger
	mu       sync.Mutex
	done     bool
}

func (p *Probation) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}

// Startup inspects <exe>.update.json. It returns (nil, nil) when no update is
// pending, a Probation when version is on trial, or ErrRestart after rolling
// back a version that has started more than maxAttempts times.
func Startup(exe, version string, window time.Duration, maxAttempts int, logger *log.Logger) (*Probation, error) {
	st, ok, err := readState(exe)
	if !ok {
		if err == nil {
			// The prior rolled-back image was locked until its process exited on Windows.
			// A fresh start has no pending probation and can remove the stale image.
			_ = os.Remove(exe + ".bad")
		}
		return nil, err
	}
	p := &Probation{Exe: exe, From: st.From, To: st.To, Window: window, Logger: logger}
	if err != nil {
		// An unreadable state file: treat the running binary as on trial.
		p.logf("%v; rolling back", err)
		return nil, p.rollback()
	}
	if st.To != version {
		// The swap did not take (or someone replaced the binary by hand).
		p.logf("update: state file names %s but this is %s; discarding it", st.To, version)
		return nil, os.Remove(statePath(exe))
	}
	st.Attempts++
	p.Attempt = st.Attempts
	if st.Attempts > maxAttempts {
		p.logf("update: %s started %d times without reaching the server; rolling back to %s", st.To, st.Attempts-1, st.From)
		return nil, p.rollback()
	}
	if err := writeState(exe, st); err != nil {
		return nil, err
	}
	p.logf("update: %s on probation (start %d of %d, %s to reach the server)", st.To, st.Attempts, maxAttempts, window)
	return p, nil
}

// Commit ends the probation: the new version stays and <exe>.prev is removed.
func (p *Probation) Commit() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return nil
	}
	p.done = true
	if err := os.Remove(statePath(p.Exe)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	os.Remove(p.Exe + ".prev")
	os.Remove(p.Exe + ".bad")
	p.logf("update: %s committed", p.To)
	return nil
}

// Rollback restores <exe>.prev, records the bad version and returns
// ErrRestart (or the error that prevented the rollback).
func (p *Probation) Rollback() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return nil
	}
	p.done = true
	return p.rollback()
}

func (p *Probation) rollback() error {
	exe, prev, bad := p.Exe, p.Exe+".prev", p.Exe+".bad"
	if _, err := os.Stat(prev); err != nil {
		os.Remove(statePath(exe))
		return fmt.Errorf("update: cannot roll back %s: %w", p.To, err)
	}
	if p.To != "" {
		if err := addSkip(exe, p.To); err != nil {
			return err
		}
	}
	// The running binary may be exe itself: move it aside (allowed on every OS
	// for a running executable), then put prev in its place.
	os.Remove(bad)
	if err := rename(exe, bad); err != nil {
		return fmt.Errorf("update: rollback: %w", err)
	}
	if err := rename(prev, exe); err != nil {
		rename(bad, exe)
		return fmt.Errorf("update: rollback: %w", err)
	}
	os.Remove(bad) // fails on Windows while bad is running; Commit cleans up later
	if err := os.Remove(statePath(exe)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	p.logf("update: rolled back %s → %s; %s will not be retried", p.To, p.From, p.To)
	return ErrRestart
}

// Watch commits when ok is closed (first successful hello) and rolls back
// when the window passes first. It returns nil after a commit, ErrRestart
// after a rollback, or ctx.Err() if ctx ends first (the next start counts as
// another attempt).
func (p *Probation) Watch(ctx context.Context, ok <-chan struct{}) error {
	t := time.NewTimer(p.Window)
	defer t.Stop()
	select {
	case <-ok:
		return p.Commit()
	case <-t.C:
		p.logf("update: %s did not reach the server within %s", p.To, p.Window)
		return p.Rollback()
	case <-ctx.Done():
		return ctx.Err()
	}
}
