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
	"runtime"
	"strings"
	"sync"
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

type Config struct {
	ServerURL string   // https, or http on a loopback host
	Token     string   // see ReadTokenFile
	Models    []string // announced in hello
	Capacity  int      // concurrent jobs; default 1
	OllamaURL string   // default ollama.DefaultURL
	Version   string   // this worker's semantic version
	Logger    *log.Logger
	Ollama    Generator // default: an ollama.Client for OllamaURL
	OnHello   func()    // called after every successful hello (optional)
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
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("worker: token file %s has mode %v; it must not be group or world accessible (chmod 600)", path, fi.Mode().Perm())
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

	mu      sync.Mutex
	maxWait time.Duration

	// draining is cancelled by Drain: no new claims, running jobs finish.
	draining context.Context
	drain    context.CancelFunc
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
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	draining, drain := context.WithCancel(context.Background())
	return &Worker{
		draining: draining, drain: drain,
		cfg: cfg, base: base, gen: gen, log: cfg.Logger,
		// Per-request deadlines come from contexts: a long-poll needs more than other calls.
		http:    &http.Client{Transport: tr},
		backoff: time.Second, maxBack: time.Minute,
		maxWait: 25 * time.Second,
	}, nil
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

// hello introduces the worker, retrying transient failures until ctx ends.
func (w *Worker) hello(ctx context.Context) error {
	req := protocol.HelloRequest{
		WorkerVersion: w.cfg.Version, ProtocolVersion: protocol.Version,
		Models: w.cfg.Models, Capacity: w.cfg.Capacity,
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
			w.log.Printf("hello: worker %s, models %v, capacity %d, max wait %s", res.WorkerID, w.cfg.Models, w.cfg.Capacity, res.MaxWait)
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

// Drain stops claiming new jobs; Run returns nil once the jobs in flight have
// finished (each is bounded by its lease deadline). Safe to call at any time.
func (w *Worker) Drain() { w.drain() }

// Run says hello and serves jobs until ctx ends, Drain is called or the server
// refuses the worker (ErrUnauthorized, *ErrUpgradeRequired).
func (w *Worker) Run(ctx context.Context) error {
	if err := w.hello(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	for range w.cfg.Capacity {
		wg.Go(func() {
			if err := w.loop(ctx); err != nil {
				cancel(err)
			}
		})
	}
	wg.Wait()
	return context.Cause(ctx)
}

func (w *Worker) loop(ctx context.Context) error {
	failures := 0
	for ctx.Err() == nil && w.draining.Err() == nil {
		w.mu.Lock()
		wait := w.maxWait
		w.mu.Unlock()
		var a protocol.Assignment
		rctx, cancel := context.WithTimeout(ctx, wait+pollMargin)
		stop := context.AfterFunc(w.draining, cancel) // Drain aborts the long-poll
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
		case err == nil && code == http.StatusNoContent:
			failures = 0
			continue
		case err == nil && code == http.StatusConflict:
			w.log.Print("server asked for hello again")
			if err := w.hello(ctx); err != nil {
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

func (w *Worker) runJob(ctx context.Context, a protocol.Assignment) {
	start := time.Now()
	jctx, cancel := context.WithDeadline(ctx, a.LeaseExpiresAt)
	out, err := w.gen.Generate(jctx, ollama.GenerateRequest{Model: a.Model, Prompt: a.Prompt, System: a.System, Format: a.Format})
	cancel()
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
