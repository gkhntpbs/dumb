package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// CredentialProvider is the private durable registry boundary. Successful
// mutations must be durable before return. Errors never escape verbatim to HTTP.
type CredentialProvider interface {
	List(context.Context, int, int) (protocol.CredentialPage, error)
	Issue(context.Context, protocol.IssueCredentialRequest) (protocol.IssuedCredential, error)
	Rotate(context.Context, string, protocol.RotateCredentialRequest) (protocol.IssuedCredential, error)
	Revoke(context.Context, string) error
	CreatePairing(context.Context, protocol.CreatePairingRequest) (protocol.PairingCode, error)
	ExchangePairing(context.Context, string) (protocol.IssuedCredential, error)
}

var (
	ErrCredentialInvalid     = errors.New("invalid credential request")
	ErrCredentialNotFound    = errors.New("credential not found")
	ErrCredentialCapacity    = errors.New("credential capacity exhausted")
	ErrCredentialUnavailable = errors.New("credential service unavailable")
	ErrPairingDenied         = errors.New("pairing denied")
)

func validAdminOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.RawFragment == "" && !u.ForceQuery && !strings.Contains(origin, "#") && u.Opaque == "" && u.String() == origin
}
func credentialTTL(v int64) bool { return v >= 1 && v <= 31622400 }

var credentialIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var pairingPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func credentialJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		writeError(w, 415, "JSON required")
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || !bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		writeError(w, 400, "invalid credential request")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "invalid credential request")
		return false
	}
	return true
}
func credentialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCredentialInvalid):
		writeError(w, 400, "invalid credential request")
	case errors.Is(err, ErrCredentialNotFound):
		writeError(w, 404, "credential not found")
	case errors.Is(err, ErrPairingDenied):
		writeError(w, 401, "unauthorized")
	default:
		writeError(w, 503, "credential service unavailable")
	}
}

func (s *Server) registerCredentials(mux *http.ServeMux, lim *limiter) {
	wrap := func(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.cfg.Credentials == nil || s.cfg.AdminSessions == nil {
				writeError(w, 503, "credential service unavailable")
				return
			}
			// Never call adminPrincipal: its legacy bearer fallback is intentionally
			// unavailable for management, even when the session provider is absent.
			p, err := s.cfg.AdminSessions.Authenticate(r)
			if err != nil || p.Role != auth.RoleAdminManage {
				writeError(w, 401, "unauthorized")
				return
			}
			if ok, after := lim.allow(p.ID); !ok {
				w.Header().Set("Retry-After", retryAfterSeconds(after))
				writeError(w, 429, "rate limit exceeded")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /admin/v1/credentials", wrap(func(w http.ResponseWriter, r *http.Request) {
		offset, limit, ok := adminPage(w, r)
		if !ok {
			return
		}
		page, err := s.cfg.Credentials.List(r.Context(), offset, limit)
		if err != nil {
			credentialError(w, err)
			return
		}
		writeJSON(w, 200, page)
	}))
	mux.HandleFunc("POST /admin/v1/credentials", wrap(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.IssueCredentialRequest
		if !credentialJSON(w, r, &req) {
			return
		}
		if (req.Role != "worker" && req.Role != "caller") || !credentialTTL(req.TTLSeconds) {
			writeError(w, 400, "invalid credential request")
			return
		}
		value, err := s.cfg.Credentials.Issue(r.Context(), req)
		if err != nil {
			credentialError(w, err)
			return
		}
		writeJSON(w, 201, value)
	}))
	mux.HandleFunc("POST /admin/v1/credentials/{id}/rotate", wrap(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.RotateCredentialRequest
		if !credentialJSON(w, r, &req) {
			return
		}
		if !credentialIDPattern.MatchString(r.PathValue("id")) || !credentialTTL(req.TTLSeconds) {
			writeError(w, 400, "invalid credential request")
			return
		}
		value, err := s.cfg.Credentials.Rotate(r.Context(), r.PathValue("id"), req)
		if err != nil {
			credentialError(w, err)
			return
		}
		writeJSON(w, 200, value)
	}))
	mux.HandleFunc("POST /admin/v1/credentials/{id}/revoke", wrap(func(w http.ResponseWriter, r *http.Request) {
		var req struct{}
		if !credentialJSON(w, r, &req) {
			return
		}
		if !credentialIDPattern.MatchString(r.PathValue("id")) {
			writeError(w, 400, "invalid credential request")
			return
		}
		if err := s.cfg.Credentials.Revoke(r.Context(), r.PathValue("id")); err != nil {
			credentialError(w, err)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /admin/v1/pairing", wrap(func(w http.ResponseWriter, r *http.Request) {
		var req protocol.CreatePairingRequest
		if !credentialJSON(w, r, &req) {
			return
		}
		if req.TTLSeconds < 1 || req.TTLSeconds > 600 || !credentialTTL(req.CredentialTTLSeconds) || (req.WorkerID != "" && !credentialIDPattern.MatchString(req.WorkerID)) {
			writeError(w, 400, "invalid credential request")
			return
		}
		value, err := s.cfg.Credentials.CreatePairing(r.Context(), req)
		if err != nil {
			credentialError(w, err)
			return
		}
		writeJSON(w, 201, value)
	}))
}

// Pairing attempts have a separate per-IP bucket, so exhausting them does not
// block already authenticated workers behind that address.
func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.cfg.Credentials == nil {
		writeError(w, 503, "pairing unavailable")
		return
	}
	if ok, after := s.pairingLim.allow(clientIP(r, s.cfg.TrustedProxies)); !ok {
		w.Header().Set("Retry-After", retryAfterSeconds(after))
		writeError(w, 429, "rate limit exceeded")
		return
	}
	version, err := protocol.ParseReleaseVersion(r.Header.Get(protocol.WorkerVersionHeader))
	if err != nil {
		writeError(w, 400, "invalid worker version")
		return
	}
	if version.Less(s.min) {
		writeJSON(w, 426, protocol.UpgradeError{Error: "worker upgrade required", MinWorkerVersion: s.min.String()})
		return
	}
	var req protocol.PairRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if !credentialJSON(w, r, &req) {
		return
	}
	if !pairingPattern.MatchString(req.Code) {
		writeError(w, 400, "invalid pairing request")
		return
	}
	value, err := s.cfg.Credentials.ExchangePairing(r.Context(), req.Code)
	if err != nil {
		credentialError(w, err)
		return
	}
	writeJSON(w, 200, value)
}

// managedCredentialPath also covers malformed suffixes, so strict Origin is
// checked before the router dispatches a management request.
func managedCredentialPath(path string) bool {
	return path == "/admin/v1/pairing" || strings.HasPrefix(path, "/admin/v1/credentials")
}
