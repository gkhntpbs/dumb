// Package worker is the dumb-worker agent loop: hello, long-poll for jobs, run
// them on the local Ollama and post the results. It makes outbound requests only.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// ErrUnauthorized means the server rejected the credential. Retrying will not help.
var ErrUnauthorized = errors.New("worker: credential rejected by the server")

// ErrUpgradeRequired means the server refuses this worker version.
type ErrUpgradeRequired struct {
	MinVersion string
}

func (e *ErrUpgradeRequired) Error() string {
	return "worker: server requires version " + e.MinVersion + " or newer"
}

// Generator runs one completion; *ollama.Client implements it.
type Generator interface {
	Generate(ctx context.Context, r ollama.GenerateRequest) (string, error)
}

// ModelLister lists the models present in Ollama; *ollama.Client implements it.
type ModelLister interface {
	List(ctx context.Context) ([]string, error)
}

// DefaultModelCheck is how often the worker re-lists Ollama's models.
const DefaultModelCheck = time.Minute

type ReadinessRecovery interface{ Ensure(context.Context) error }

type Status struct {
	Error  bool
	Ready  bool
	Active int
}

type Config struct {
	OnStatus  func(Status)
	Recovery  ReadinessRecovery
	Transport http.RoundTripper
	ServerURL string   // https, or http on a loopback host
	Token     string   // see ReadTokenFile
	Models    []string // configured; hello announces those Ollama actually has
	Capacity  int      // concurrent jobs; default 1
	OllamaURL string   // default ollama.DefaultURL
	Version   string   // this worker's semantic version
	Logger    *log.Logger
	Ollama    Generator // default: an ollama.Client for OllamaURL
	OnHello   func()    // called after every successful hello, or server check while no model is present (optional)

	// Lister is asked every ModelCheck which models Ollama has. Default: the
	// Ollama generator when it can list (an *ollama.Client); nil Lister and a
	// generator that cannot list mean the configured models are announced as is.
	Lister     ModelLister
	ModelCheck time.Duration // default DefaultModelCheck
}

// CheckServerURL requires https unless the host is loopback.
func CheckServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("worker: server URL %q is not an absolute URL", raw)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && ollama.IsLoopbackHost(u.Hostname()):
	default:
		return nil, fmt.Errorf("worker: server URL %q must use https (http is allowed only on loopback)", raw)
	}
	return u, nil
}

// ReadTokenFile reads a bearer token, refusing a file others can read.
func ReadTokenFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("worker: token file is required")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("worker: %w", err)
	}
	if err := checkTokenPermissions(path, fi); err != nil {
		return "", err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("worker: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("worker: token file %s is empty", path)
	}
	return tok, nil
}

type Worker struct {
	cfg     Config
	base    *url.URL
	http    *http.Client
	gen     Generator
	log     *log.Logger
	backoff time.Duration // first retry delay; doubles up to maxBackoff
	maxBack time.Duration

	statusMu       sync.Mutex
	active         atomic.Int64
	readinessError atomic.Bool

	mu      sync.Mutex
	maxWait time.Duration
	models  []string      // announced; empty = Ollama has none of the configured models
	changed chan struct{} // closed and replaced whenever models changes

	// draining is cancelled by Drain: no new claims, running jobs finish.
	draining   context.Context
	drain      context.CancelFunc
	readiness  chan struct{}
	authorized atomic.Bool
}

func New(cfg Config) (*Worker, error) {
	base, err := CheckServerURL(cfg.ServerURL)
	if err != nil {
		return nil, err
	}
	if cfg.Token == "" {
		return nil, errors.New("worker: token is required")
	}
	if len(cfg.Models) == 0 {
		return nil, errors.New("worker: at least one model is required")
	}
	if _, err := protocol.ParseReleaseVersion(cfg.Version); err != nil {
		return nil, fmt.Errorf("worker: %w", err)
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = 1
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(io.Discard, "", 0)
	}
	gen := cfg.Ollama
	if gen == nil {
		c, err := ollama.New(cfg.OllamaURL)
		if err != nil {
			return nil, err
		}
		gen = c
	}
	if cfg.Lister == nil {
		if l, ok := gen.(ModelLister); ok {
			cfg.Lister = l
		}
	}
	if cfg.ModelCheck <= 0 {
		cfg.ModelCheck = DefaultModelCheck
	}
	var transport http.RoundTripper = cfg.Transport
	if transport == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		tr.TLSHandshakeTimeout = 10 * time.Second
		transport = tr
	}
	draining, drain := context.WithCancel(context.Background())
	return &Worker{
		draining: draining, drain: drain,
		cfg: cfg, base: base, gen: gen, log: cfg.Logger,
		// Per-request deadlines come from contexts: a long-poll needs more than other calls.
		http:      &http.Client{Transport: transport},
		readiness: make(chan struct{}, 1),
		backoff:   time.Second, maxBack: time.Minute,
		maxWait: 25 * time.Second,
		models:  slices.Clone(cfg.Models),
		changed: make(chan struct{}),
	}, nil
}

// current returns the announced models and a channel closed when they change.
func (w *Worker) current() ([]string, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.models, w.changed
}

func (w *Worker) setModels(m []string) {
	w.readinessError.Store(false)
	w.mu.Lock()
	w.models = m
	close(w.changed)
	w.changed = make(chan struct{})
	w.mu.Unlock()
	w.reportStatus()
}

func (w *Worker) reportStatus() {
	w.statusMu.Lock()
	defer w.statusMu.Unlock()
	if w.cfg.OnStatus == nil {
		return
	}
	w.mu.Lock()
	status := Status{Error: w.readinessError.Load(), Ready: len(w.models) > 0, Active: int(w.active.Load())}
	w.mu.Unlock()
	w.cfg.OnStatus(status)
}

// present lists Ollama's models and returns the configured ones it has, in
// configuration order, plus the full list (for logs).
func (w *Worker) present(ctx context.Context) ([]string, []string, error) {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	have, err := w.cfg.Lister.List(lctx)
	if err != nil {
		return nil, nil, err
	}
	if observer, ok := w.cfg.Recovery.(interface{ ObserveReady() }); ok {
		observer.ObserveReady()
	}
	var out []string
	for _, m := range w.cfg.Models {
		if ollama.HasModel(have, m) {
			out = append(out, m)
		}
	}
	return out, have, nil
}

func (w *Worker) logNoModel(have []string) {
	w.log.Printf("no configured model is present in Ollama (configured %v, Ollama has %v); not claiming jobs until one appears", w.cfg.Models, have)
}

// checkIn is the server contact of a worker with no model to announce: GET
// /v1/workers/me changes nothing server-side but still reports a revoked
// credential (401) or an outdated version (426), so updates and probation keep
// working. One attempt; a transient failure is logged and retried next tick.
func (w *Worker) checkIn(ctx context.Context) error {
	w.authorized.Store(false)
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	code, err := w.call(rctx, http.MethodGet, "/v1/workers/me", "", nil, nil)
	cancel()
	switch {
	case fatal(err):
		return err
	case err == nil && code == http.StatusOK:
		w.authorized.Store(true)
		if w.cfg.OnHello != nil {
			w.cfg.OnHello()
		}
		return nil
	case ctx.Err() != nil:
		return nil
	case err == nil:
		err = fmt.Errorf("unexpected status %d", code)
	}
	w.log.Printf("server check failed: %v", err)
	return nil
}

func (w *Worker) unavailable(ctx context.Context) {
	w.mu.Lock()
	hadModels := len(w.models) > 0
	if hadModels {
		w.models = nil
		close(w.changed)
		w.changed = make(chan struct{})
	}
	w.mu.Unlock()
	w.reportStatus()
	if hadModels {
		w.goodbye(ctx)
	}
}

func (w *Worker) watchModels(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.ModelCheck)
	defer ticker.Stop()
	immediate := w.cfg.Recovery != nil
	forced := false
	for {
		if !immediate {
			select {
			case <-ctx.Done():
				return nil
			case <-w.draining.Done():
				return nil
			case <-w.readiness:
				forced = true
			case <-ticker.C:
			}
		}
		immediate = false
		models, have, err := w.present(ctx)
		repair := forced || err != nil || len(models) < len(w.cfg.Models)
		forced = false
		if err != nil || (repair && w.cfg.Recovery != nil) {
			w.unavailable(ctx)
		}
		if repair && w.cfg.Recovery != nil {
			if err := w.checkIn(ctx); err != nil {
				return err
			}
			if !w.authorized.Load() {
				continue
			}
			repairCtx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(w.draining, cancel)
			w.readinessError.Store(false)
			w.reportStatus()
			err = w.cfg.Recovery.Ensure(repairCtx)
			stop()
			cancel()
			if ctx.Err() != nil || w.draining.Err() != nil {
				return nil
			}
			if err != nil {
				w.readinessError.Store(true)
				w.reportStatus()
				w.log.Printf("Ollama unavailable; recovery deferred: %v", err)
				continue
			}
			models, have, err = w.present(ctx)
		}
		if err != nil {
			w.unavailable(ctx)
			w.log.Printf("listing Ollama models failed; not claiming jobs: %v", err)
			if err := w.checkIn(ctx); err != nil {
				return err
			}
			continue
		}
		current, _ := w.current()
		if slices.Equal(models, current) {
			if len(models) == 0 {
				if err := w.checkIn(ctx); err != nil {
					return err
				}
			}
			continue
		}
		if len(models) == 0 {
			w.logNoModel(have)
			w.unavailable(ctx)
			continue
		}
		if err := w.hello(ctx, models); err != nil {
			if fatal(err) {
				return err
			}
			return nil
		}
		w.setModels(models)
	}
}

// pollMargin is added to the long-poll wait for the request's own timeout.
const pollMargin = 15 * time.Second

// requestTimeout bounds every request other than the long-poll.
const requestTimeout = 30 * time.Second

// errRetry marks a transient failure (network, 5xx).
type errRetry struct{ err error }

func (e errRetry) Error() string { return e.err.Error() }
func (e errRetry) Unwrap() error { return e.err }

// call sends one request and decodes a 200 JSON body into out (if non-nil).
// It returns the status code; 401 and 426 become their typed errors.
func (w *Worker) call(ctx context.Context, method, path, query string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	u := w.base.JoinPath(path)
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	req.Header.Set(protocol.WorkerVersionHeader, w.cfg.Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := w.http.Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return 0, err
		}
		return 0, errRetry{err}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return res.StatusCode, ErrUnauthorized
	case res.StatusCode == http.StatusUpgradeRequired:
		var ue protocol.UpgradeError
		json.Unmarshal(b, &ue)
		return res.StatusCode, &ErrUpgradeRequired{MinVersion: ue.MinWorkerVersion}
	case res.StatusCode >= 500:
		return res.StatusCode, errRetry{fmt.Errorf("%s %s: %s", method, path, res.Status)}
	case res.StatusCode == http.StatusOK && out != nil:
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, errRetry{fmt.Errorf("%s %s: %w", method, path, err)}
		}
	}
	return res.StatusCode, nil
}

// fatal reports whether err must stop the worker.
func fatal(err error) bool {
	var up *ErrUpgradeRequired
	return errors.Is(err, ErrUnauthorized) || errors.As(err, &up)
}

// sleep waits for the attempt'th backoff (capped, full jitter) or ctx.
func (w *Worker) sleep(ctx context.Context, attempt int) error {
	d := w.backoff << min(attempt, 16)
	if d <= 0 || d > w.maxBack {
		d = w.maxBack
	}
	d = d/2 + rand.N(d/2+1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// hello introduces the worker with models, retrying transient failures until ctx ends.
func (w *Worker) hello(ctx context.Context, models []string) error {
	req := protocol.HelloRequest{
		WorkerVersion: w.cfg.Version, ProtocolVersion: protocol.Version,
		Models: models, Capacity: w.cfg.Capacity, Features: []string{protocol.FeatureAck},
	}
	for attempt := 0; ; attempt++ {
		var res protocol.HelloResponse
		rctx, cancel := context.WithTimeout(ctx, requestTimeout)
		code, err := w.call(rctx, http.MethodPost, "/v1/workers/hello", "", req, &res)
		cancel()
		switch {
		case err == nil && code == http.StatusOK:
			if d, err := time.ParseDuration(res.MaxWait); err == nil && d > 0 {
				w.mu.Lock()
				w.maxWait = d
				w.mu.Unlock()
			}
			w.log.Printf("hello: worker %s, models %v, capacity %d, max wait %s", res.WorkerID, models, w.cfg.Capacity, res.MaxWait)
			if w.cfg.OnHello != nil {
				w.cfg.OnHello()
			}
			return nil
		case fatal(err):
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		case err == nil:
			// 400 and the like will not fix themselves, but a server upgrade might.
			err = fmt.Errorf("hello: unexpected status %d", code)
		}
		w.log.Printf("hello failed (attempt %d): %v", attempt+1, err)
		if err := w.sleep(ctx, attempt); err != nil {
			return err
		}
	}
}

// goodbye tells the server this worker is leaving the pool, so callers get
// 503 at once instead of waiting for the online timeout. Best effort: one
// short attempt that works even while ctx is ending.
func (w *Worker) goodbye(ctx context.Context) {
	gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), goodbyeTimeout)
	defer cancel()
	code, err := w.call(gctx, http.MethodPost, "/v1/workers/goodbye", "", nil, nil)
	switch {
	case err != nil:
		w.log.Printf("goodbye failed: %v", err)
	case code != http.StatusNoContent:
		w.log.Printf("goodbye: unexpected status %d", code)
	}
}

// goodbyeTimeout bounds the goodbye request: shutdown must not hang on it.
const goodbyeTimeout = 3 * time.Second

// Drain stops claiming new jobs; Run returns nil once the jobs in flight have
// finished (each is bounded by its lease deadline). Safe to call at any time.
func (w *Worker) Drain() { w.drain() }

// Run says hello and serves jobs until ctx ends, Drain is called or the server
// refuses the worker (ErrUnauthorized, *ErrUpgradeRequired).
//
// Only the configured models Ollama actually has are announced, re-checked
// every ModelCheck; with none present the worker waits without claiming.
func (w *Worker) Run(ctx context.Context) error {
	if w.cfg.Recovery != nil {
		if err := w.checkIn(ctx); err != nil {
			return err
		}
	}
	models := slices.Clone(w.cfg.Models)
	if w.cfg.Lister != nil {
		m, have, err := w.present(ctx)
		switch {
		case err != nil:
			w.log.Printf("listing Ollama models failed; not announcing unavailable models: %v", err)
			models = nil
		case len(m) == 0:
			w.logNoModel(have)
			models = nil
		default:
			models = m
		}
	}
	if len(models) > 0 {
		if err := w.hello(ctx, models); err != nil {
			return err
		}
	} else if err := w.checkIn(ctx); err != nil {
		return err
	}
	w.setModels(models)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	if w.cfg.Lister != nil {
		wg.Go(func() {
			if err := w.watchModels(ctx); err != nil {
				cancel(err)
			}
		})
	}
	for range w.cfg.Capacity {
		wg.Go(func() {
			if err := w.loop(ctx); err != nil {
				cancel(err)
			}
		})
	}
	wg.Wait()
	err := context.Cause(ctx)
	// Leaving for any reason but a refused credential or version (where the
	// server would refuse goodbye too): stop counting as online now.
	if cur, _ := w.current(); len(cur) > 0 && !fatal(err) {
		w.goodbye(ctx)
	}
	return err
}

func (w *Worker) loop(ctx context.Context) error {
	failures := 0
	for ctx.Err() == nil && w.draining.Err() == nil {
		models, changed := w.current()
		if len(models) == 0 { // nothing to serve: wait for a model to appear
			select {
			case <-ctx.Done():
			case <-w.draining.Done():
			case <-changed:
			}
			continue
		}
		w.mu.Lock()
		wait := w.maxWait
		w.mu.Unlock()
		var a protocol.Assignment
		rctx, cancel := context.WithTimeout(ctx, wait+pollMargin)
		stop := context.AfterFunc(w.draining, cancel) // Drain aborts the long-poll
		go func() {                                   // so does a model change: the poll was for the old set
			select {
			case <-changed:
				cancel()
			case <-rctx.Done():
			}
		}()
		code, err := w.call(rctx, http.MethodGet, "/v1/jobs/next", "wait="+wait.String(), nil, &a)
		stop()
		cancel()
		switch {
		case fatal(err):
			return err
		case err == nil && code == http.StatusOK:
			// A job that arrived is run even when draining or stopping: runJob
			// reports it either way, so the caller is not left waiting.
			failures = 0
			w.runJob(ctx, a)
			continue
		case ctx.Err() != nil, w.draining.Err() != nil:
			return nil
		case err != nil && isClosed(changed):
			continue
		case err == nil && code == http.StatusNoContent:
			failures = 0
			continue
		case err == nil && code == http.StatusConflict:
			w.log.Print("server asked for hello again")
			if err := w.hello(ctx, models); err != nil {
				if fatal(err) {
					return err
				}
				return nil
			}
			continue
		case err == nil:
			err = fmt.Errorf("poll: unexpected status %d", code)
		}
		w.log.Printf("poll failed: %v", err)
		if w.sleep(ctx, failures) != nil {
			return nil
		}
		failures++
	}
	return nil
}

func isClosed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func (w *Worker) ack(ctx context.Context, a protocol.Assignment) bool {
	actx, cancel := context.WithTimeout(ctx, ackTimeout)
	defer cancel()
	code, err := w.call(actx, http.MethodPost, "/v1/jobs/"+a.JobID+"/ack", "", protocol.AckRequest{LeaseID: a.LeaseID}, nil)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			w.log.Printf("job %s: ack failed: %v", a.JobID, err)
		}
	case code == http.StatusConflict:
		return true
	case code != http.StatusNoContent:
		w.log.Printf("job %s: ack: unexpected status %d", a.JobID, code)
	}
	return false
}

// ackTimeout bounds the ack request.
const ackTimeout = 5 * time.Second

func (w *Worker) runJob(ctx context.Context, a protocol.Assignment) {
	w.active.Add(1)
	w.reportStatus()
	defer func() { w.active.Add(-1); w.reportStatus() }()
	start := time.Now()
	jctx, cancel := context.WithDeadline(ctx, a.LeaseExpiresAt)
	// Ack in parallel with the generation, so it adds no latency. A refused
	// ack means the lease was revoked (it arrived too late): stop the job.
	var revoked atomic.Bool
	acked := make(chan struct{})
	firstAck := make(chan struct{})
	go func() {
		defer close(acked)
		refused := w.ack(jctx, a)
		close(firstAck)
		if refused {
			revoked.Store(true)
			cancel()
			return
		}
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-jctx.Done():
				return
			case <-ticker.C:
			}
			if w.ack(jctx, a) {
				revoked.Store(true)
				cancel()
				return
			}
		}
	}()
	limit := a.NumPredict
	if limit < 1 || limit > protocol.MaxNumPredict {
		limit = protocol.MaxNumPredict
	}
	out, err := w.gen.Generate(jctx, ollama.GenerateRequest{Model: a.Model, Prompt: a.Prompt, System: a.System, Format: a.Format, NumPredict: limit})
	if err == nil {
		<-firstAck
	}
	cancel()
	<-acked
	if err != nil && ctx.Err() == nil && !errors.Is(err, context.DeadlineExceeded) && !revoked.Load() {
		if recovery, ok := w.cfg.Recovery.(interface{ ReportFailure(error) }); ok {
			recovery.ReportFailure(err)
		}
		w.unavailable(ctx)
		select {
		case w.readiness <- struct{}{}:
		default:
		}
	}
	if revoked.Load() {
		w.log.Printf("job %s: lease revoked; dropped", a.JobID)
		return
	}
	res := protocol.ResultRequest{LeaseID: a.LeaseID, Output: out}
	if err != nil {
		res = protocol.ResultRequest{LeaseID: a.LeaseID, Error: err.Error()}
		if ctx.Err() != nil {
			res.Error = "worker shutting down"
		}
	}
	// Post even while shutting down so the caller learns now, not at the deadline.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	code, perr := w.call(pctx, http.MethodPost, "/v1/jobs/"+a.JobID+"/result", "", res, nil)
	switch {
	case perr != nil:
		w.log.Printf("job %s: posting result failed: %v", a.JobID, perr)
	case code != http.StatusNoContent:
		w.log.Printf("job %s: result refused with status %d", a.JobID, code)
	case res.Error != "":
		w.log.Printf("job %s: failed after %s: %s", a.JobID, time.Since(start).Round(time.Millisecond), res.Error)
	default:
		w.log.Printf("job %s: done in %s", a.JobID, time.Since(start).Round(time.Millisecond))
	}
}
