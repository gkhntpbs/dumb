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

// recommendCooldown is how long a server-recommended version that did not
// install (not released yet, failed verification, ...) waits before another
// hello carrying the same recommendation may trigger a check again.
const recommendCooldown = time.Hour

// supervisor runs the agent loop and decides when to update and restart.
// Update triggers: startup, every `every` (+ jitter), a server-recommended
// version newer than this one, and a 426 from the server.
type supervisor struct {
	version   string
	newWorker func(onHello func()) (runner, error)
	upd       updater           // nil: self-update disabled
	probation *update.Probation // non-nil while this version is on trial
	every     time.Duration     // periodic update check
	jitter    time.Duration
	backoff   time.Duration // first wait after a 426 with no usable update; doubles
	maxBack   time.Duration
	controls  <-chan string
	uiFailed  <-chan struct{}
	status    func(string, string)
	log       *log.Logger

	// Server recommendations: the worker's hello hands them over through
	// recommend (latest wins); the update goroutine acts on them.
	recommend     chan string
	recMu         sync.Mutex
	recLast       string    // last recommendation that triggered a check
	recAt         time.Time // when it did
	recIgnored    string    // last recommendation logged as ignored
	recommendWait time.Duration
}

// recommended receives a server-recommended version; it never blocks.
func (s *supervisor) recommended(v string) {
	if s.recommend == nil {
		return
	}
	for {
		select {
		case s.recommend <- v:
			return
		default:
		}
		select { // replace an older pending recommendation
		case <-s.recommend:
		default:
		}
	}
}

// followRecommendation reports whether a recommended version warrants an
// immediate update check: it must be newer than this version, not one this
// worker rolled back, and not the same version checked within the cooldown.
func (s *supervisor) followRecommendation(v string) bool {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	ignore := func(why string) bool {
		if s.recIgnored != v {
			s.recIgnored = v
			s.log.Printf("update: server recommends %s; %s", v, why)
		}
		return false
	}
	rv, err := protocol.ParseReleaseVersion(v)
	if err != nil {
		return ignore("not a valid version; ignored")
	}
	cur, err := protocol.ParseReleaseVersion(s.version)
	if err != nil || !cur.Less(rv) {
		return false
	}
	if sk, ok := s.upd.(interface{ Skipped(string) bool }); ok && sk.Skipped(rv.String()) {
		return ignore("that version was rolled back here and is not retried")
	}
	wait := s.recommendWait
	if wait <= 0 {
		wait = recommendCooldown
	}
	if rv.String() == s.recLast && time.Since(s.recAt) < wait {
		return false
	}
	s.recLast, s.recAt = rv.String(), time.Now()
	return true
}

// run returns the process exit code: exitRestart after an applied update or
// a rollback, exitOK when ctx ends, exitError on a fatal error.
func (s *supervisor) run(parent context.Context) int {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	if s.recommend == nil {
		s.recommend = make(chan string, 1)
	}
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
		select {
		case <-s.uiFailed:
			s.log.Print("menu helper unavailable; no jobs will be claimed")
			s.report("error", "ui")
			<-ctx.Done()
			return stoppedCode(ctx)
		default:
		}
		s.report("starting", "none")
		w, err := s.newWorker(onHello)
		if err != nil {
			s.log.Print(err)
			return exitError
		}
		var applied atomic.Bool
		pctx, stopPeriodic := context.WithCancel(ctx)
		go s.periodic(pctx, func() { applied.Store(true); w.Drain() })
		result := make(chan error, 1)
		go func() { result <- w.Run(ctx) }()
		mode := ""
	running:
		for {
			select {
			case err = <-result:
				break running
			case control := <-s.controls:
				if mode != "" {
					if control == "quit" && mode == "paused" {
						mode = "quit"
						s.report("stopping", "none")
					}
					continue
				}
				switch control {
				case "pause":
					mode = "paused"
					s.report("draining", "none")
					w.Drain()
				case "quit":
					mode = "quit"
					s.report("stopping", "none")
					w.Drain()
				}
			case <-s.uiFailed:
				mode = "ui"
				s.log.Print("menu helper unavailable; draining jobs and stopping claims")
				s.report("draining", "ui")
				w.Drain()
				s.uiFailed = nil
			}
		}
		stopPeriodic()
		if mode == "quit" && err == nil {
			return exitOK
		}
		if mode == "ui" && !applied.Load() && ctx.Err() == nil {
			s.report("error", "ui")
			<-ctx.Done()
		}
		if mode == "paused" && err == nil && !applied.Load() && ctx.Err() == nil {
			s.report("paused", "none")
		paused:
			for {
				select {
				case control := <-s.controls:
					if control == "resume" {
						s.report("starting", "none")
						break paused
					}
					if control == "quit" {
						return exitOK
					}
				case <-s.uiFailed:
					s.report("error", "ui")
					s.uiFailed = nil
					<-ctx.Done()
					return stoppedCode(ctx)
				case <-ctx.Done():
					return exitOK
				}
			}
			continue
		}
		var up *worker.ErrUpgradeRequired
		switch {
		case applied.Load():
			s.log.Print("jobs drained; restarting into the new version")
			return exitRestart
		case errors.Is(context.Cause(ctx), update.ErrRestart):
			return exitRestart
		case errors.As(err, &up):
			s.report("error", "upgrade")
			s.log.Printf("UPGRADE REQUIRED: the server accepts version %s or newer; this is %s", up.MinVersion, s.version)
			if s.tryUpdate(ctx, up.MinVersion, "upgrade required") {
				return exitRestart
			}
			// Exiting would make the service manager restart us in a tight loop.
			s.log.Printf("no usable update for %s; staying up and checking again in %s", up.MinVersion, backoff)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			case control := <-s.controls:
				if control == "quit" {
					return exitOK
				}
			case <-s.uiFailed:
				s.report("error", "ui")
				<-ctx.Done()
				return exitOK
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
			category := "backend"
			if errors.Is(err, worker.ErrUnauthorized) {
				category = "credentials"
			}
			s.report("error", category)
			s.log.Print(err)
			if s.controls != nil {
				for {
					select {
					case control := <-s.controls:
						if control == "quit" {
							return exitOK
						}
					case <-s.uiFailed:
						s.report("error", "ui")
						<-ctx.Done()
						return exitOK
					case <-ctx.Done():
						return exitOK
					}
				}
			}
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
	s.report("updating", "none")
	v, err := s.upd.Update(ctx, min)
	s.report("", "none")
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

// periodic checks every s.every plus up to s.jitter, and at once when the
// server recommends a newer version, and calls applied (once) after an update
// has been installed. One goroutine runs every check, so they never overlap.
func (s *supervisor) periodic(ctx context.Context, applied func()) {
	if s.upd == nil {
		return
	}
	var tick <-chan time.Time
	next := func() {
		if s.every <= 0 {
			return
		}
		d := s.every
		if s.jitter > 0 {
			d += rand.N(s.jitter)
		}
		tick = time.After(d)
	}
	next()
	for {
		why := "periodic"
		select {
		case <-ctx.Done():
			return
		case <-tick:
			next()
		case v := <-s.recommend:
			if !s.followRecommendation(v) {
				continue
			}
			why = "server recommends " + v
		}
		if s.tryUpdate(ctx, "", why) {
			applied()
			return
		}
	}
}

func (s *supervisor) report(state, category string) {
	if s.status != nil {
		s.status(state, category)
	}
}

func stoppedCode(ctx context.Context) int {
	if errors.Is(context.Cause(ctx), update.ErrRestart) {
		return exitRestart
	}
	return exitOK
}
