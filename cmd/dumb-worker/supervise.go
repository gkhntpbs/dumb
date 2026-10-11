package main

import (
	"context"
	"errors"
	"log"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/update"
	"github.com/gkhntpbs/dumb/internal/worker"
)

// runner is the agent loop; *worker.Worker implements it.
type runner interface {
	Run(ctx context.Context) error
	Drain()
}

// updater applies a release; *update.Updater implements it.
type updater interface {
	Update(ctx context.Context, min string) (protocol.ReleaseVersion, error)
}

// supervisor runs the agent loop and decides when to update and restart.
// Update triggers: startup, every `every` (+ jitter), and a 426 from the server.
type supervisor struct {
	version   string
	newWorker func(onHello func()) (runner, error)
	upd       updater           // nil: self-update disabled
	probation *update.Probation // non-nil while this version is on trial
	every     time.Duration     // periodic update check
	jitter    time.Duration
	backoff   time.Duration // first wait after a 426 with no usable update; doubles
	maxBack   time.Duration
	log       *log.Logger
}

// run returns the process exit code: exitRestart after an applied update or
// a rollback, exitOK when ctx ends, exitError on a fatal error.
func (s *supervisor) run(parent context.Context) int {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	helloOK := make(chan struct{})
	var once sync.Once
	onHello := func() { once.Do(func() { close(helloOK) }) }

	if s.probation != nil {
		go func() {
			err := s.probation.Watch(ctx, helloOK)
			switch {
			case errors.Is(err, update.ErrRestart):
				cancel(update.ErrRestart)
			case err != nil && ctx.Err() == nil:
				s.log.Printf("update: probation: %v", err)
			}
		}()
	} else if s.tryUpdate(ctx, "", "startup") {
		return exitRestart
	}

	backoff := s.backoff
	for {
		w, err := s.newWorker(onHello)
		if err != nil {
			s.log.Print(err)
			return exitError
		}
		var applied atomic.Bool
		pctx, stopPeriodic := context.WithCancel(ctx)
		go s.periodic(pctx, func() { applied.Store(true); w.Drain() })
		err = w.Run(ctx)
		stopPeriodic()
		var up *worker.ErrUpgradeRequired
		switch {
		case applied.Load():
			s.log.Print("jobs drained; restarting into the new version")
			return exitRestart
		case errors.Is(context.Cause(ctx), update.ErrRestart):
			return exitRestart
		case errors.As(err, &up):
			s.log.Printf("UPGRADE REQUIRED: the server accepts version %s or newer; this is %s", up.MinVersion, s.version)
			if s.tryUpdate(ctx, up.MinVersion, "upgrade required") {
				return exitRestart
			}
			// Exiting would make the service manager restart us in a tight loop.
			s.log.Printf("no usable update for %s; staying up and checking again in %s", up.MinVersion, backoff)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			if ctx.Err() != nil {
				if errors.Is(context.Cause(ctx), update.ErrRestart) {
					return exitRestart
				}
				s.log.Print("stopped")
				return exitOK
			}
			backoff = min(backoff*2, s.maxBack)
			continue
		case ctx.Err() != nil:
			if errors.Is(context.Cause(ctx), update.ErrRestart) {
				return exitRestart
			}
			s.log.Print("stopped")
			return exitOK
		case err != nil:
			s.log.Print(err)
			return exitError
		}
		return exitOK
	}
}

// tryUpdate reports whether an update was applied.
func (s *supervisor) tryUpdate(ctx context.Context, min, why string) bool {
	if s.upd == nil {
		if min != "" {
			s.log.Print(update.ErrDisabled)
		}
		return false
	}
	v, err := s.upd.Update(ctx, min)
	switch {
	case err == nil:
		s.log.Printf("update (%s): installed %s", why, v)
		return true
	case errors.Is(err, update.ErrNoUpdate), errors.Is(err, update.ErrPending):
		s.log.Printf("update (%s): %v", why, err)
	case ctx.Err() != nil:
	default:
		s.log.Printf("update (%s) failed; keeping %s: %v", why, s.version, err)
	}
	return false
}

// periodic checks every s.every plus up to s.jitter and calls applied (once)
// after an update has been installed.
func (s *supervisor) periodic(ctx context.Context, applied func()) {
	if s.upd == nil || s.every <= 0 {
		return
	}
	for {
		d := s.every
		if s.jitter > 0 {
			d += rand.N(s.jitter)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
		if s.tryUpdate(ctx, "", "periodic") {
			applied()
			return
		}
	}
}
