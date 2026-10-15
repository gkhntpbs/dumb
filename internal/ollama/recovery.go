package ollama

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var ErrExternalEndpoint = errors.New("ollama: configured or previously running endpoint is unmanaged; waiting for its owner")
var ErrOccupiedEndpoint = errors.New("ollama: endpoint is occupied or unresponsive; refusing to install or start over it")
var ErrRecoveryBackoff = errors.New("ollama: recovery is cooling down")

type RecoveryProgress struct {
	Stage     string
	Model     string
	Completed int64
	Total     int64
}

type RecoveryConfig struct {
	Progress                                                                                              func(RecoveryProgress)
	System                                                                                                *System
	Models                                                                                                []string
	External                                                                                              bool
	Attempts                                                                                              int
	ProbeTimeout, StartTimeout, InstallTimeout, PullTimeout, Backoff, Cooldown, MaxCooldown, PollInterval time.Duration
	Logger                                                                                                *log.Logger
}

type Recovery struct {
	cfg      RecoveryConfig
	mu       sync.Mutex
	external bool
	next     time.Time
	failures int
	restart  atomic.Bool
}

func NewRecovery(cfg RecoveryConfig) (*Recovery, error) {
	if cfg.System == nil || cfg.System.Client == nil || len(cfg.Models) == 0 {
		return nil, errors.New("ollama: recovery requires a system, client and configured models")
	}
	for _, model := range cfg.Models {
		if model == "" {
			return nil, errors.New("ollama: recovery model is empty")
		}
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = 3
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 3 * time.Second
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 30 * time.Second
	}
	if cfg.InstallTimeout <= 0 {
		cfg.InstallTimeout = 5 * time.Minute
	}
	if cfg.PullTimeout <= 0 {
		cfg.PullTimeout = 15 * time.Minute
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = time.Second
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = time.Minute
	}
	if cfg.MaxCooldown <= 0 {
		cfg.MaxCooldown = 10 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	external := cfg.External || (cfg.System.GOOS != "darwin" && cfg.System.GOOS != "windows") || cfg.System.Client.base.String() != DefaultURL
	return &Recovery{cfg: cfg, external: external}, nil
}

func (r *Recovery) probe(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	defer cancel()
	_, err := r.cfg.System.Client.Version(probe)
	return err
}

func (r *Recovery) progress(stage, model string, completed, total int64) {
	if r.cfg.Progress != nil {
		r.cfg.Progress(RecoveryProgress{Stage: stage, Model: model, Completed: completed, Total: total})
	}
}

func (r *Recovery) attempt(ctx context.Context) error {
	r.progress("checking", "", 0, 0)
	system := r.cfg.System
	if system.Client.remote {
		return r.checkRemote(ctx)
	}
	if r.restart.Swap(false) && system.Owned() {
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := system.StopOwned(stop)
		cancel()
		if err != nil {
			return err
		}
	}
	err := r.probe(ctx)
	if err == nil {
		r.progress("reachable", "", 0, 0)
		if !system.Owned() {
			r.external = true
		}
	} else {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.external {
			return ErrExternalEndpoint
		}
		if system.Owned() {
			stop, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = system.StopOwned(stop)
			cancel()
			if err != nil {
				return err
			}
			err = r.probe(ctx)
			if err == nil {
				r.external = true
			}
		}
		if err != nil {
			if !isConnectionRefused(err) {
				return fmt.Errorf("%w: %v", ErrOccupiedEndpoint, err)
			}
			r.progress("detecting", "", 0, 0)
			detected := system.Detect(ctx)
			if detected.Installed {
				r.progress("found", "", 0, 0)
			}
			if !detected.Installed {
				r.progress("installing", "", 0, 0)
				install, cancel := context.WithTimeout(ctx, r.cfg.InstallTimeout)
				err = system.Install(install)
				cancel()
				if err != nil {
					return fmt.Errorf("ollama: install: %w", err)
				}
				r.progress("installed", "", 0, 0)
			}
			r.progress("starting", "", 0, 0)
			if err := system.Start(ctx); err != nil {
				return fmt.Errorf("ollama: start: %w", err)
			}
			start, cancel := context.WithTimeout(ctx, r.cfg.StartTimeout)
			_, err = system.WaitReachable(start, r.cfg.PollInterval)
			cancel()
			if err != nil {
				return err
			}
		}
	}
	r.progress("api-ready", "", 0, 0)
	probe, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	models, err := system.Client.List(probe)
	cancel()
	if err != nil {
		if system.Owned() {
			stop, cancel := context.WithTimeout(ctx, 5*time.Second)
			stopErr := system.StopOwned(stop)
			cancel()
			return errors.Join(err, stopErr)
		}
		return err
	}
	for _, model := range r.cfg.Models {
		if HasModel(models, model) {
			r.progress("model-ready", model, 0, 0)
			continue
		}
		pull, cancel := context.WithTimeout(ctx, r.cfg.PullTimeout)
		r.progress("pulling", model, 0, 0)
		err = system.Client.Pull(pull, model, func(p Progress) { r.progress("pulling", model, p.Completed, p.Total) })
		cancel()
		if err != nil {
			return fmt.Errorf("ollama: pull configured model: %w", err)
		}
	}
	probe, cancel = context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	models, err = system.Client.List(probe)
	cancel()
	if err != nil {
		return err
	}
	for _, model := range r.cfg.Models {
		if !HasModel(models, model) {
			return errors.New("ollama: configured model absent after recovery")
		}
	}
	r.progress("ready", "", 0, 0)
	return nil
}

// checkRemote is recovery for a remote Ollama: it only checks reachability and
// the configured models. Nothing is installed, started, stopped or pulled; the
// caller stops advertising and retries with backoff.
func (r *Recovery) checkRemote(ctx context.Context) error {
	if err := r.probe(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %w", ErrRemoteUnavailable, err)
	}
	r.progress("reachable", "", 0, 0)
	r.progress("api-ready", "", 0, 0)
	probe, cancel := context.WithTimeout(ctx, r.cfg.ProbeTimeout)
	models, err := r.cfg.System.Client.List(probe)
	cancel()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRemoteUnavailable, err)
	}
	for _, model := range r.cfg.Models {
		if !HasModel(models, model) {
			return fmt.Errorf("%w: %s", ErrRemoteModelMissing, model)
		}
		r.progress("model-ready", model, 0, 0)
	}
	r.progress("ready", "", 0, 0)
	return nil
}

func permanentRecovery(err error) bool {
	var api *APIError
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, ErrExternalEndpoint) || errors.Is(err, ErrOccupiedEndpoint) || errors.Is(err, ErrRemoteModelMissing) || errors.Is(err, ErrNotPrivate) || (errors.As(err, &api) && api.Status >= 400 && api.Status < 500 && api.Status != 429)
}

func (r *Recovery) Ensure(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Now().Before(r.next) {
		return ErrRecoveryBackoff
	}
	var err error
	for attempt := 0; attempt < r.cfg.Attempts; attempt++ {
		err = r.attempt(ctx)
		if err == nil {
			r.failures = 0
			r.next = time.Time{}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.cfg.Logger.Printf("Ollama recovery attempt %d/%d failed: %v", attempt+1, r.cfg.Attempts, err)
		if permanentRecovery(err) || attempt+1 == r.cfg.Attempts {
			break
		}
		delay := r.cfg.Backoff << min(attempt, 16)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(delay, r.cfg.MaxCooldown)):
		}
	}
	r.failures++
	delay := r.cfg.Cooldown << min(r.failures-1, 16)
	r.next = time.Now().Add(min(delay, r.cfg.MaxCooldown))
	return err
}

func (r *Recovery) Close(ctx context.Context) error {
	if r.cfg.System.Client.remote {
		return nil
	}
	return r.cfg.System.StopOwned(ctx)
}

func (r *Recovery) ReportFailure(err error) {
	var api *APIError
	if (errors.As(err, &api) && api.Status >= 500) || isConnectionRefused(err) {
		r.restart.Store(true)
	}
}

func (r *Recovery) ObserveReady() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.cfg.System.Owned() {
		r.external = true
	}
}
