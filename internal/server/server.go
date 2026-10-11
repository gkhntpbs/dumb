// Package server exposes the queue over HTTP as described in docs/protocol/openapi.yaml.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

type Config struct {
	Auth             auth.Authenticator
	MinWorkerVersion string        // e.g. "0.1.0"
	MaxWait          time.Duration // cap on GET /v1/jobs/next?wait=; default 25s
	OnlineTTL        time.Duration // a worker is online this long after its last request; default MaxWait+10s
}

type worker struct {
	models   []string
	capacity int
	lastSeen time.Time
	polling  int
}

type Server struct {
	cfg     Config
	min     protocol.SemVer
	q       *queue.Queue
	mux     *http.ServeMux
	mu      sync.Mutex
	workers map[string]*worker
}

func New(cfg Config) (*Server, error) {
	if cfg.Auth == nil {
		return nil, errors.New("server: Auth is required")
	}
	min, err := protocol.ParseSemVer(cfg.MinWorkerVersion)
	if err != nil {
		return nil, err
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 25 * time.Second
	}
	if cfg.OnlineTTL <= 0 {
		cfg.OnlineTTL = cfg.MaxWait + 10*time.Second
	}
	s := &Server{cfg: cfg, min: min, q: queue.New(), mux: http.NewServeMux(), workers: map[string]*worker{}}
	s.mux.HandleFunc("POST /v1/workers/hello", s.asWorker(s.hello))
	s.mux.HandleFunc("GET /v1/jobs/next", s.asWorker(s.claim))
	s.mux.HandleFunc("POST /v1/jobs/{id}/result", s.asWorker(s.result))
	s.mux.HandleFunc("POST /v1/jobs", s.asCaller(s.submit))
	s.mux.HandleFunc("GET /v1/jobs/{id}", s.asCaller(s.getJob))
	s.mux.HandleFunc("GET /v1/status", s.asCaller(s.status))
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	s.mux.ServeHTTP(w, r)
}

type handler func(w http.ResponseWriter, r *http.Request, p auth.Principal)

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, role auth.Role) (auth.Principal, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p, err := s.cfg.Auth.Authenticate(token)
	if !ok || err != nil || p.Role != role {
		writeJSON(w, http.StatusUnauthorized, protocol.Error{Error: "unauthorized"})
		return p, false
	}
	return p, true
}

func (s *Server) asWorker(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.authenticate(w, r, auth.RoleWorker)
		if !ok {
			return
		}
		v, err := protocol.ParseSemVer(r.Header.Get(protocol.WorkerVersionHeader))
		if err != nil {
			writeError(w, http.StatusBadRequest, protocol.WorkerVersionHeader+": "+err.Error())
			return
		}
		if v.Less(s.min) {
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
		wk = &worker{}
		s.workers[p.ID] = wk
	}
	wk.models, wk.capacity, wk.lastSeen = slices.Clone(req.Models), req.Capacity, time.Now()
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, protocol.HelloResponse{
		WorkerID:         p.ID,
		MinWorkerVersion: s.min.String(),
		MaxWait:          s.cfg.MaxWait.String(),
	})
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
	models := wk.models
	wk.polling++
	wk.lastSeen = time.Now()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		wk.polling--
		wk.lastSeen = time.Now()
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	a, err := s.q.Claim(ctx, p.ID, models)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case a == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, a)
	}
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
	if err := s.q.Complete(r.PathValue("id"), req.LeaseID, p.ID, req.Output, req.Error); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
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
	case req.Format != "" && req.Format != "json":
		writeError(w, http.StatusBadRequest, `format must be "" or "json"`)
		return
	}
	if !slices.Contains(s.Status().Models, req.Model) {
		writeError(w, http.StatusServiceUnavailable, "no online worker serves "+req.Model)
		return
	}
	s.waitAndWrite(w, r, s.q.Submit(req), wait)
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
	now := time.Now()
	for _, wk := range s.workers {
		if wk.polling == 0 && now.Sub(wk.lastSeen) > s.cfg.OnlineTTL {
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
		wk.lastSeen = time.Now()
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
