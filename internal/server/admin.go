package server

import (
	"encoding/json"
	"github.com/gkhntpbs/dumb/internal/branding"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server/adminui"
)

// AdminSessionProvider is the browser authentication boundary. Implementations
// verify passwords, issue/revoke opaque cookies and enforce session expiry.
// Production implementations belong outside this public repository.
type AdminSessionProvider interface {
	Authenticate(*http.Request) (auth.Principal, error)
	Login(http.ResponseWriter, *http.Request, string) error
	Logout(http.ResponseWriter, *http.Request)
}

func (s *Server) adminPrincipal(r *http.Request) (auth.Principal, error) {
	if s.cfg.AdminSessions != nil {
		return s.cfg.AdminSessions.Authenticate(r)
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	return s.cfg.Auth.Authenticate(token)
}

// Proxy headers are intentionally ignored: the embedding application must
// preserve the public Host. Both schemes are accepted for TLS-terminating proxies;
// the production provider is responsible for enforcing its HTTPS public origin.
func adminSameOrigin(r *http.Request, required bool) bool {
	origin := r.Header.Get("Origin")
	return r.Header.Get("Sec-Fetch-Site") != "cross-site" &&
		((!required && origin == "") || origin == "http://"+r.Host || origin == "https://"+r.Host)
}

func ListenAdmin(address string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return nil, &net.AddrError{Err: "admin listener requires a numeric loopback address", Addr: address}
	}
	return net.Listen("tcp", address)
}

func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	files, _ := fs.Sub(adminui.Files, "assets")
	assets := http.FileServer(http.FS(files))
	mux.Handle("GET /{$}", assets)
	mux.Handle("GET /admin.js", assets)
	mux.Handle("GET /admin.css", assets)
	mux.Handle("GET /admin-contract.js", assets)
	mux.Handle("GET /chart.umd.js", assets)
	branding.Register(mux)
	lim := newLimiter(DefaultCallerRate, time.Now)
	denied := newLimiter(DefaultUnauthRate, time.Now)
	s.registerCredentials(mux, lim)
	mux.HandleFunc("/admin/v1/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, 405, "method not allowed")
			return
		}
		p, err := s.adminPrincipal(r)
		writeJSON(w, 200, protocol.AdminSession{Authenticated: err == nil && (p.Role == auth.RoleAdminRead || p.Role == auth.RoleAdminManage)})
	})
	mux.HandleFunc("/admin/v1/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, 405, "method not allowed")
			return
		}
		if s.cfg.AdminSessions == nil {
			writeError(w, 503, "browser login unavailable")
			return
		}
		ip := clientIP(r, nil)
		if ok, after := denied.allow(ip); !ok {
			w.Header().Set("Retry-After", retryAfterSeconds(after))
			writeError(w, 429, "rate limit exceeded")
			return
		}
		typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || typ != "application/json" {
			writeError(w, 415, "JSON required")
			return
		}
		var body protocol.AdminLogin
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if dec.Decode(&body) != nil || len(body.Password) == 0 || len(body.Password) > 512 || dec.Decode(&struct{}{}) != io.EOF {
			writeError(w, 400, "invalid login request")
			return
		}
		if s.cfg.AdminSessions.Login(w, r, body.Password) != nil {
			writeError(w, 401, "unauthorized")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/admin/v1/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, 405, "method not allowed")
			return
		}
		if s.cfg.AdminSessions == nil {
			writeError(w, 503, "browser login unavailable")
			return
		}
		s.cfg.AdminSessions.Logout(w, r)
		w.WriteHeader(http.StatusNoContent)
	})
	for _, path := range []string{"summary", "workers", "queue", "api"} {
		mux.HandleFunc("/admin/v1/"+path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				writeError(w, 405, "method not allowed")
				return
			}

			ip := clientIP(r, nil)
			if blocked, after := denied.blocked(ip); blocked {
				w.Header().Set("Retry-After", retryAfterSeconds(after))
				writeError(w, 429, "rate limit exceeded")
				return
			}
			p, err := s.adminPrincipal(r)
			if err != nil || (p.Role != auth.RoleAdminRead && p.Role != auth.RoleAdminManage) {
				denied.allow(ip)
				s.log.Warn("admin access denied")
				writeError(w, 401, "unauthorized")
				return
			}
			if ok, after := lim.allow(p.ID); !ok {
				w.Header().Set("Retry-After", retryAfterSeconds(after))
				writeError(w, 429, "rate limit exceeded")
				return
			}
			offset, limit, ok := adminPage(w, r)
			if !ok {
				return
			}
			model, state := r.URL.Query().Get("model"), r.URL.Query().Get("state")
			if len(model) > 512 {
				writeError(w, 400, "model filter too long")
				return
			}
			switch path {
			case "api":
				writeJSON(w, 200, s.api.snapshot(s.cfg.Now().UTC(), s.epoch))
			case "summary":
				if state != "" {
					writeError(w, 400, "summary does not accept state")
					return
				}
				o := s.q.Observe("", "", 0, 0)
				workers := s.adminWorkers(model, "")
				summary := protocol.AdminSummary{SnapshotAt: o.At, Epoch: s.epoch, Queued: o.Queued, Leased: o.Leased, Done: o.Done, Failed: o.Failed, Expired: o.Expired, Cancelled: o.Cancelled, OldestQueuedMS: o.OldestQueuedMS, LatencySamples: o.Samples, MeanWaitMS: o.MeanWait, MeanCompletionMS: o.MeanCompletion, MeasurementWindowSeconds: 300, Models: []string{}}
				for _, wk := range workers {
					if wk.State == "offline" {
						continue
					}
					summary.WorkersOnline++
					summary.Capacity += wk.Capacity
					summary.ActiveSlots += wk.LiveLeases
					summary.FreeSlots += wk.FreeSlots
					for _, m := range wk.Models {
						if len(summary.Models) < 100 && !slices.Contains(summary.Models, m) {
							summary.Models = append(summary.Models, m)
						}
					}
				}
				slices.Sort(summary.Models)
				writeJSON(w, 200, summary)
			case "workers":
				if state != "" && !slices.Contains([]string{"online-idle", "online-busy", "offline"}, state) {
					writeError(w, 400, "invalid worker state")
					return
				}
				rows := s.adminWorkers(model, state)
				total := len(rows)
				rows = rows[min(offset, total):min(offset+limit, total)]
				writeJSON(w, 200, protocol.AdminWorkers{SnapshotAt: s.cfg.Now().UTC(), Epoch: s.epoch, Total: total, Offset: offset, Limit: limit, Items: rows})
			case "queue":
				if state != "" && !slices.Contains([]string{protocol.StateQueued, protocol.StateLeased, protocol.StateDone, protocol.StateFailed, protocol.StateExpired, protocol.StateCancelled}, state) {
					writeError(w, 400, "invalid job state")
					return
				}
				o := s.q.Observe(model, state, offset, limit)
				writeJSON(w, 200, protocol.AdminQueue{SnapshotAt: o.At, Epoch: s.epoch, Total: o.Total, Offset: offset, Limit: limit, Items: o.Jobs})
			}
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if managedCredentialPath(r.URL.Path) && (r.Header.Get("Origin") != s.cfg.AdminOrigin || s.cfg.AdminOrigin == "" || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			writeError(w, 403, "cross-origin request denied")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/v1/") && !adminSameOrigin(r, (r.URL.Path == "/admin/v1/login" || r.URL.Path == "/admin/v1/logout") && r.Method == http.MethodPost) {
			writeError(w, 403, "cross-origin request denied")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		mux.ServeHTTP(w, r)
	})
}

func adminPage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	vals := []int{0, 50}
	for i, key := range []string{"offset", "limit"} {
		if raw := r.URL.Query().Get(key); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || i == 0 && (v < 0 || v > 10000) || i == 1 && (v < 1 || v > 100) {
				writeError(w, 400, "invalid pagination")
				return 0, 0, false
			}
			vals[i] = v
		}
	}
	return vals[0], vals[1], true
}

func boundedText(v string, limit int) string {
	r := []rune(v)
	if len(r) > limit {
		r = r[:limit]
	}
	return string(r)
}

func (s *Server) adminWorkers(model, state string) []protocol.AdminWorker {
	leases := s.q.Observe("", "", 0, 0).Leases
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []protocol.AdminWorker{}
	now := s.cfg.Now()
	for id, wk := range s.workers {
		if model != "" && !slices.Contains(wk.models, model) {
			continue
		}
		online := len(wk.models) > 0 && (wk.polling > 0 || now.Sub(wk.lastSeen) <= s.cfg.OnlineTTL)
		status := "offline"
		if online {
			status = "online-idle"
			if leases[id] > 0 {
				status = "online-busy"
			}
		}
		if state != "" && state != status {
			continue
		}
		models := []string{}
		for _, m := range wk.models[:min(len(wk.models), 100)] {
			models = append(models, boundedText(m, 128))
		}
		free := 0
		if online {
			free = max(0, wk.capacity-leases[id])
		}
		rows = append(rows, protocol.AdminWorker{WorkerID: boundedText(id, 128), WorkerVersion: boundedText(wk.version, 64), Models: models, Capacity: wk.capacity, LiveLeases: leases[id], FreeSlots: free, LastSeen: wk.lastSeen, State: status})
	}
	slices.SortFunc(rows, func(a, b protocol.AdminWorker) int { return strings.Compare(a.WorkerID, b.WorkerID) })
	return rows
}
