package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func randomAdminToken() string { b := make([]byte, 32); rand.Read(b); return hex.EncodeToString(b) }
func adminFixture(t *testing.T) (*Server, *auth.StaticTokens, string) {
	t.Helper()
	a := auth.NewStaticTokens()
	v := randomAdminToken()
	a.Add("admin", auth.RoleAdminRead, v)
	s, e := New(Config{Auth: a, MinWorkerVersion: "0.0.0"})
	if e != nil {
		t.Fatal(e)
	}
	return s, a, v
}
func adminGet(s *Server, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(w, r)
	return w
}
func TestAdminAuthorizationIsolation(t *testing.T) {
	s, a, v := adminFixture(t)
	for _, role := range []auth.Role{auth.RoleCaller, auth.RoleWorker} {
		token := randomAdminToken()
		a.Add(string(role), role, token)
		if adminGet(s, "/admin/v1/summary", token).Code != 401 {
			t.Fatal(role)
		}
	}
	for _, token := range []string{"", randomAdminToken()} {
		if adminGet(s, "/admin/v1/summary", token).Code != 401 {
			t.Fatal("missing/invalid")
		}
	}
	a.Revoke("admin")
	if adminGet(s, "/admin/v1/summary", v).Code != 401 {
		t.Fatal("revoked")
	}
	a.AddUntil("admin", auth.RoleAdminRead, v, time.Now().Add(-time.Second))
	if adminGet(s, "/admin/v1/summary", v).Code != 401 {
		t.Fatal("expired")
	}
	a.Add("admin", auth.RoleAdminRead, v)
	for _, path := range []string{"/admin/v1/summary", "/admin/v1/workers", "/admin/v1/queue"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+v)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatal("public listener", w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "OPTIONS", "HEAD"} {
		r := httptest.NewRequest(method, "/admin/v1/summary", nil)
		r.Header.Set("Authorization", "Bearer "+v)
		w := httptest.NewRecorder()
		s.AdminHandler().ServeHTTP(w, r)
		if w.Code != 405 {
			t.Fatal(method, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/admin/v1/summary", nil)
	r.Header.Set("Authorization", "Bearer "+v)
	r.Header.Set("Origin", "https://untrusted.invalid")
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("origin", w.Code)
	}
}
func TestAdminListenerGuard(t *testing.T) {
	for _, addr := range []string{":0", "localhost:0", "0.0.0.0:0", "[::]:0", "192.0.2.1:0"} {
		l, e := ListenAdmin(addr)
		if e == nil {
			l.Close()
			t.Fatal(addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		l, e := ListenAdmin(addr)
		if e != nil {
			t.Fatal(e)
		}
		l.Close()
	}
}
func TestAdminLifecycleSanitization(t *testing.T) {
	s, _, v := adminFixture(t)
	s.workers["w"] = &worker{models: []string{"m", "other"}, version: "1.2.3", capacity: 2, lastSeen: time.Now(), gone: make(chan struct{})}
	secret := randomAdminToken()
	id := s.q.Submit(protocol.SubmitRequest{Model: "m", Prompt: secret, System: secret, DeadlineMS: 10000})
	lease, _ := s.q.Claim(context.Background(), "w", []string{"m"}, 2)
	check := func(online, capacity, active, free, leased, done int) {
		t.Helper()
		w := adminGet(s, "/admin/v1/summary", v)
		var p protocol.AdminSummary
		if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		if p.WorkersOnline != online || p.Capacity != capacity || p.ActiveSlots != active || p.FreeSlots != free || p.Leased != leased || p.Done != done {
			t.Fatalf("%+v", p)
		}
	}
	check(1, 2, 1, 1, 1, 0)
	for _, path := range []string{"summary", "workers", "queue"} {
		w := adminGet(s, "/admin/v1/"+path, v)
		if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), lease.LeaseID) {
			t.Fatal("secret leak")
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("headers")
		}
	}
	s.mu.Lock()
	delete(s.workers, "w")
	s.mu.Unlock()
	check(0, 0, 0, 0, 1, 0)
	if e := s.q.Complete(id, lease.LeaseID, "w", secret, ""); e != nil {
		t.Fatal(e)
	}
	check(0, 0, 0, 0, 0, 1)
	for _, q := range []string{"limit=101", "offset=10001", "offset=-1", "state=unknown"} {
		if adminGet(s, "/admin/v1/queue?"+q, v).Code != 400 {
			t.Fatal(q)
		}
	}
	other, _, _ := adminFixture(t)
	if s.epoch.Equal(other.epoch) {
		t.Fatal("epoch")
	}
}
