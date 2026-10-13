// Package server exposes the queue over HTTP as described in docs/protocol/openapi.yaml.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/queue"
)

// MaxBodyBytes bounds every request body.
const MaxBodyBytes = 1 << 20

// DefaultAckWindow is Config.AckWindow's default.
const DefaultAckWindow = 2 * time.Second

type Config struct {
	AdminSessions    AdminSessionProvider // optional browser authentication; implementation belongs to the embedding application
	Auth             auth.Authenticator
	MinWorkerVersion string           // e.g. "0.1.0"
	MaxWait          time.Duration    // cap on GET /v1/jobs/next?wait=; default 25s
	OnlineTTL        time.Duration    // a worker is online this long after its last request; default MaxWait+10s
	Now              func() time.Time // clock for worker freshness and admin observations; default time.Now
	Logger           *slog.Logger     // default: discard. Never receives tokens, prompts or outputs.
	// AckWindow is how long a worker that announced the "ack" feature has to
	// acknowledge an assignment before its lease is revoked and the job goes
	// back to the front of the queue. Never beyond the job's deadline.
	// Default DefaultAckWindow.
	AckWindow time.Duration

	// Rate limits (token buckets, in memory): per worker principal, per caller
	// principal, and per client IP for requests that fail authentication.
	WorkerRate, CallerRate, UnauthRate Rate
	// TrustedProxies are the peers whose X-Forwarded-For is believed when
	// finding the client IP. Default none: the TCP peer address is used.
	TrustedProxies []netip.Prefix
}

type worker struct {
	version  string
	models   []string
	capacity int
	lastSeen time.Time
	polling  int
	ack      bool          // announced protocol.FeatureAck
	gone     chan struct{} // closed by goodbye: aborts this worker's polls
}

type Server struct {
	api     apiMetrics
	epoch   time.Time
	cfg     Config
	min     protocol.ReleaseVersion
	q       *queue.Queue
	mux     *http.ServeMux
	mu      sync.Mutex
	workers map[string]*worker
	log     *slog.Logger

	workerLim, callerLim, unauthLim *limiter

	// afterClaim, if set, runs after a job is claimed and before the
	// assignment is written (a test seam for the aborted long-poll race).
	afterClaim func(r *http.Request)
}

func New(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, errors.New("server: Auth is required")
	}
	min, err := protocol.ParseReleaseVersion(cfg.MinWorkerVersion)
	if err != nil {
		return nil, err
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 25 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.OnlineTTL <= 0 {
		cfg.OnlineTTL = cfg.MaxWait + 10*time.Second
	}
	if cfg.AckWindow <= 0 {
		cfg.AckWindow = DefaultAckWindow
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	cfg.WorkerRate = cfg.WorkerRate.orDefault(DefaultWorkerRate)
	cfg.CallerRate = cfg.CallerRate.orDefault(DefaultCallerRate)
	cfg.UnauthRate = cfg.UnauthRate.orDefault(DefaultUnauthRate)
	s := &Server{epoch: cfg.Now().UTC(), cfg: cfg, min: min, q: queue.New(), mux: http.NewServeMux(), workers: map[string]*worker{}, log: cfg.Logger,
		workerLim: newLimiter(cfg.WorkerRate, time.Now),
		callerLim: newLimiter(cfg.CallerRate, time.Now),
		unauthLim: newLimiter(cfg.UnauthRate, time.Now),
	}
	s.q.SetClock(cfg.Now)
	s.q.OnExpire = func(jobID, model, workerID string) {
		if workerID == "" {
			s.log.Warn("job expired", "job_id", jobID, "model", model, "was", protocol.StateQueued)
		} else {
			s.log.Warn("job expired", "job_id", jobID, "model", model, "was", protocol.StateLeased, "worker", workerID)
		}
	}
	s.mux.HandleFunc("POST /v1/workers/hello", s.asWorker(s.hello))
	s.mux.HandleFunc("POST /v1/workers/goodbye", s.asWorker(s.goodbye))
	s.mux.HandleFunc("GET /v1/workers/me", s.asWorker(s.whoami))
	s.mux.HandleFunc("GET /v1/jobs/next", s.asWorker(s.claim))
	s.mux.HandleFunc("POST /v1/jobs/{id}/ack", s.asWorker(s.ack))
	s.mux.HandleFunc("POST /v1/jobs/{id}/result", s.asWorker(s.result))
	s.mux.HandleFunc("POST /v1/jobs", s.asCaller(s.submit))
	s.mux.HandleFunc("GET /v1/jobs/{id}", s.asCaller(s.getJob))
	s.mux.HandleFunc("GET /v1/status", s.asCaller(s.status))
	return s, nil
}

// RequestIDHeader carries the request id; a sane incoming value is kept,
// otherwise one is generated. It is echoed on every response.
const RequestIDHeader = "X-Request-ID"

var saneRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

type ctxKey struct{}

// reqLog returns the request-scoped logger (with request_id).
func (s *Server) reqLog(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return s.log
}

// statusWriter records the status code for the access log.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (sw *statusWriter) WriteHeader(code int) {
	if sw.code == 0 {
		sw.code = code
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if sw.code == 0 {
		sw.code = http.StatusOK
	}
	return sw.ResponseWriter.Write(b)
}

func (sw *statusWriter) Unwrap() http.ResponseWriter { return sw.ResponseWriter }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := r.Header.Get(RequestIDHeader)
	if !saneRequestID.MatchString(id) {
		b := make([]byte, 8)
		rand.Read(b)
		id = hex.EncodeToString(b)
	}
	w.Header().Set(RequestIDHeader, id)
	l := s.log.With("request_id", id)
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, l))
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	sw := &statusWriter{ResponseWriter: w}
	route := apiRoute(r)
	s.api.begin(route)
	defer func() {
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		s.api.end(route, s.cfg.Now(), code, time.Since(start))
	}()
	s.mux.ServeHTTP(sw, r)
	// The path, never the query or body; job ids are not secret.
	l.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.code,
		"duration_ms", time.Since(start).Milliseconds(), "request_bytes", max(r.ContentLength, 0))
}

type handler func(w http.ResponseWriter, r *http.Request, p auth.Principal)

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, role auth.Role) (auth.Principal, bool) {
	ip := clientIP(r, s.cfg.TrustedProxies)
	// An IP that keeps failing is refused before its token is even looked at,
	// so guessing costs the attacker a 429 per try, not a 401.
	if blocked, after := s.unauthLim.blocked(ip); blocked {
		s.tooMany(w, r, after, "client_ip", ip, "reason", "failed authentication")
		return auth.Principal{}, false
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p, err := s.cfg.Auth.Authenticate(token)
	if !ok || err != nil || p.Role != role {
		s.unauthLim.allow(ip)
		s.reqLog(r).Warn("unauthorized", "path", r.URL.Path, "client_ip", ip, "role", string(role))
		writeJSON(w, http.StatusUnauthorized, protocol.Error{Error: "unauthorized"})
		return p, false
	}
	lim := s.workerLim
	if role == auth.RoleCaller {
		lim = s.callerLim
	}
	if ok, after := lim.allow(p.ID); !ok {
		s.tooMany(w, r, after, "principal", p.ID, "role", string(role))
		return p, false
	}
	return p, true
}

// tooMany answers 429 with Retry-After.
func (s *Server) tooMany(w http.ResponseWriter, r *http.Request, after time.Duration, attrs ...any) {
	ra := retryAfterSeconds(after)
	s.reqLog(r).Warn("rate limited", append([]any{"path", r.URL.Path, "retry_after_s", ra}, attrs...)...)
	w.Header().Set("Retry-After", ra)
	writeError(w, http.StatusTooManyRequests, "rate limit exceeded; retry after "+ra+"s")
}

func (s *Server) asWorker(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(w, r, auth.RoleWorker)
		if !ok {
			return
		}
		v, err := protocol.ParseReleaseVersion(r.Header.Get(protocol.WorkerVersionHeader))
		if err != nil {
			writeError(w, http.StatusBadRequest, protocol.WorkerVersionHeader+": "+err.Error())
			return
		}
		if v.Less(s.min) {
			s.reqLog(r).Info("upgrade required", "worker", p.ID, "worker_version", v.String(), "min_worker_version", s.min.String())
			writeJSON(w, http.StatusUpgradeRequired, protocol.UpgradeError{
				Error:            "worker " + v.String() + " is older than the minimum " + s.min.String() + "; update before retrying",
				MinWorkerVersion: s.min.String(),
			})
			return
		}
		h(w, r, p)
	}
}

func (s *Server) asCaller(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := s.authenticate(w, r, auth.RoleCaller); ok {
			h(w, r, p)
		}
	}
}

func (s *Server) hello(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req protocol.HelloRequest
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.ProtocolVersion != protocol.Version:
		writeError(w, http.StatusBadRequest, "unsupported protocol_version")
		return
	case len(req.Models) == 0:
		writeError(w, http.StatusBadRequest, "models must not be empty")
		return
	case req.Capacity < 1:
		writeError(w, http.StatusBadRequest, "capacity must be at least 1")
		return
	}
	s.mu.Lock()
	wk := s.workers[p.ID]
	if wk == nil {
		wk = &worker{gone: make(chan struct{})}
		s.workers[p.ID] = wk
	}
	wk.models, wk.capacity, wk.lastSeen = slices.Clone(req.Models), req.Capacity, s.cfg.Now()
	wk.version = req.WorkerVersion
	wk.ack = slices.Contains(req.Features, protocol.FeatureAck)
	s.mu.Unlock()
	s.reqLog(r).Info("worker hello", "worker", p.ID, "worker_version", req.WorkerVersion, "models", req.Models,
		"capacity", req.Capacity, "features", req.Features)
	writeJSON(w, http.StatusOK, protocol.HelloResponse{
		WorkerID:         p.ID,
		MinWorkerVersion: s.min.String(),
		MaxWait:          s.cfg.MaxWait.String(),
	})
}

// goodbye takes the worker offline at once: it is no longer counted by
// Status (callers get 503 instead of waiting for OnlineTTL) and its polls in
// flight end. Unknown workers get 204 too, so a retry is harmless.
func (s *Server) goodbye(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	s.mu.Lock()
	wk := s.workers[p.ID]
	if wk != nil {
		delete(s.workers, p.ID)
		close(wk.gone)
	}
	s.mu.Unlock()
	if wk != nil {
		s.reqLog(r).Info("worker goodbye", "worker", p.ID, "leases", s.q.Leases(p.ID))
	}
	w.WriteHeader(http.StatusNoContent)
}

// whoami confirms the credential and version; unlike hello it changes nothing.
func (s *Server) whoami(w http.ResponseWriter, _ *http.Request, p auth.Principal) {
	writeJSON(w, http.StatusOK, protocol.WhoAmI{WorkerID: p.ID, MinWorkerVersion: s.min.String()})
}

func (s *Server) claim(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wait, ok := parseWait(w, r, 25*time.Second)
	if !ok {
		return
	}
	wait = min(wait, s.cfg.MaxWait)
	s.mu.Lock()
	wk := s.workers[p.ID]
	if wk == nil {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "send hello first")
		return
	}
	models, capacity, needAck := wk.models, wk.capacity, wk.ack
	wk.polling++
	wk.lastSeen = s.cfg.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		wk.polling--
		wk.lastSeen = s.cfg.Now()
		s.mu.Unlock()
	}()

	// Beyond its capacity a poll waits, like one with no job: it returns the
	// moment one of the worker's leases ends and a job is queued, or 204.
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	go func() { // goodbye ends the poll
		select {
		case <-wk.gone:
			cancel()
		case <-ctx.Done():
		}
	}()
	a, err := s.q.Claim(ctx, p.ID, models, capacity)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	case a == nil:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.afterClaim != nil {
		s.afterClaim(r)
	}
	l := s.reqLog(r)
	// The poll may have been aborted in the instant the job was claimed. Then
	// nobody would run it until its deadline: put it back at the front.
	if r.Context().Err() == nil {
		writeJSON(w, http.StatusOK, a)
		err = http.NewResponseController(w).Flush()
	}
	if err = errors.Join(err, r.Context().Err()); err != nil {
		requeued := s.q.Release(a.JobID, a.LeaseID)
		l.Warn("claim not delivered", "job_id", a.JobID, "worker", p.ID, "requeued", requeued, "err", err)
		return
	}
	if needAck {
		// A response flushed into a dead connection reports no error. A worker
		// that acks proves it got the job; without the ack in time, the job
		// goes back to the front for another worker.
		window := min(s.cfg.AckWindow, time.Until(a.LeaseExpiresAt))
		jobID, leaseID, log := a.JobID, a.LeaseID, s.log
		time.AfterFunc(window, func() {
			if s.q.RevokeUnacked(jobID, leaseID) {
				log.Warn("lease revoked: no ack", "job_id", jobID, "worker", p.ID, "ack_window", window.String())
			}
		})
	}
	l.Info("job claimed", "job_id", a.JobID, "worker", p.ID, "model", a.Model,
		"prompt_bytes", len(a.Prompt), "system_bytes", len(a.System), "lease_expires_at", a.LeaseExpiresAt, "ack", needAck)
}

func (s *Server) ack(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req protocol.AckRequest
	if !decode(w, r, &req) {
		return
	}
	if req.LeaseID == "" {
		writeError(w, http.StatusBadRequest, "lease_id is required")
		return
	}
	s.touch(p.ID)
	id := r.PathValue("id")
	if err := s.q.Ack(id, req.LeaseID, p.ID); err != nil {
		s.reqLog(r).Info("ack refused", "job_id", id, "worker", p.ID, "err", err)
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.reqLog(r).Debug("job acked", "job_id", id, "worker", p.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var req protocol.ResultRequest
	if !decode(w, r, &req) {
		return
	}
	if req.LeaseID == "" {
		writeError(w, http.StatusBadRequest, "lease_id is required")
		return
	}
	if req.Output != "" && req.Error != "" {
		writeError(w, http.StatusBadRequest, "send output or error, not both")
		return
	}
	s.touch(p.ID)
	id := r.PathValue("id")
	if err := s.q.Complete(id, req.LeaseID, p.ID, req.Output, req.Error); err != nil {
		s.reqLog(r).Info("result refused", "job_id", id, "worker", p.ID, "err", err)
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	state := protocol.StateDone
	if req.Error != "" {
		state = protocol.StateFailed
	}
	s.reqLog(r).Info("job result", "job_id", id, "worker", p.ID, "state", state,
		"output_bytes", len(req.Output), "error_bytes", len(req.Error))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wait, ok := parseWait(w, r, 0)
	if !ok {
		return
	}
	var req protocol.SubmitRequest
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Model == "" || req.Prompt == "":
		writeError(w, http.StatusBadRequest, "model and prompt are required")
		return
	case req.DeadlineMS < 1 || req.DeadlineMS > protocol.MaxDeadlineMS:
		writeError(w, http.StatusBadRequest, "deadline_ms out of range")
		return
	case req.NumPredict < 0 || req.NumPredict > protocol.MaxNumPredict:
		writeError(w, http.StatusBadRequest, "num_predict out of range")
		return
	case req.Format != "" && req.Format != "json":
		writeError(w, http.StatusBadRequest, `format must be "" or "json"`)
		return
	}
	if !slices.Contains(s.Status().Models, req.Model) {
		s.reqLog(r).Info("no worker for model", "caller", p.ID, "model", req.Model)
		writeError(w, http.StatusServiceUnavailable, "no online worker serves "+req.Model)
		return
	}
	if req.NumPredict == 0 {
		req.NumPredict = protocol.MaxNumPredict
	}
	id := s.q.Submit(req)
	s.reqLog(r).Info("job submitted", "job_id", id, "caller", p.ID, "model", req.Model,
		"prompt_bytes", len(req.Prompt), "system_bytes", len(req.System), "deadline_ms", req.DeadlineMS)
	s.waitAndWrite(w, r, id, wait)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	wait, ok := parseWait(w, r, 0)
	if !ok {
		return
	}
	s.waitAndWrite(w, r, r.PathValue("id"), wait)
}

func (s *Server) waitAndWrite(w http.ResponseWriter, r *http.Request, id string, wait time.Duration) {
	deadline, err := s.q.Deadline(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if until := time.Until(deadline); wait > until {
		wait = max(until, 0)
	}
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	st, err := s.q.Wait(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	code := http.StatusAccepted
	switch st.State {
	case protocol.StateDone, protocol.StateFailed:
		code = http.StatusOK
	case protocol.StateExpired:
		code = http.StatusGone
	}
	writeJSON(w, code, st)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request, _ auth.Principal) {
	writeJSON(w, http.StatusOK, s.Status())
}

// Status reports the workers that are online and the models they serve.
func (s *Server) Status() protocol.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := protocol.Status{Models: []string{}}
	now := s.cfg.Now()
	for _, wk := range s.workers {
		if len(wk.models) == 0 || wk.polling == 0 && now.Sub(wk.lastSeen) > s.cfg.OnlineTTL {
			continue
		}
		st.WorkersOnline++
		for _, m := range wk.models {
			if !slices.Contains(st.Models, m) {
				st.Models = append(st.Models, m)
			}
		}
	}
	slices.Sort(st.Models)
	return st
}

func (s *Server) touch(id string) {
	s.mu.Lock()
	if wk := s.workers[id]; wk != nil {
		wk.lastSeen = s.cfg.Now()
	}
	s.mu.Unlock()
}

func parseWait(w http.ResponseWriter, r *http.Request, def time.Duration) (time.Duration, bool) {
	v := r.URL.Query().Get("wait")
	if v == "" {
		return def, true
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		writeError(w, http.StatusBadRequest, "wait must be a non-negative Go duration")
		return 0, false
	}
	return d, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	// Unknown fields are ignored: adding a field does not bump protocol.Version.
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, protocol.Error{Error: msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
