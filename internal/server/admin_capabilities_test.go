package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
)

// The session endpoint tells the admin panel what the signed-in session may do,
// so "Add worker" can explain a disabled or read-only state instead of failing
// on a 403 from the strict management origin check.
func TestAdminSessionCapabilities(t *testing.T) {
	session := func(t *testing.T, role auth.Role, adjust func(*Config), cookie bool) (string, protocol.AdminSession) {
		t.Helper()
		e := newEnvWith(t, func(c *Config) {
			c.AdminSessions = &fixtureAdminSessions{role: role}
			if adjust != nil {
				adjust(c)
			}
		})
		r := httptest.NewRequest("GET", "https://admin.invalid/admin/v1/session", nil)
		if cookie {
			r.AddCookie(&http.Cookie{Name: "dumb_admin_session", Value: randomAdminToken()})
		}
		w := httptest.NewRecorder()
		e.srv.AdminHandler().ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("session: %d %q", w.Code, w.Header().Get("Cache-Control"))
		}
		var got protocol.AdminSession
		dec := json.NewDecoder(strings.NewReader(w.Body.String()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&got); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(w.Body.String()), got
	}
	managed := func(c *Config) {
		c.Credentials = &fixtureCredentials{}
		c.AdminOrigin = "https://admin.invalid"
		c.PublicURL = "https://dumb.example/"
	}

	t.Run("anonymous learns nothing about the configuration", func(t *testing.T) {
		body, _ := session(t, auth.RoleAdminManage, managed, false)
		if body != `{"authenticated":false}` {
			t.Fatalf("anonymous body %s", body)
		}
	})
	t.Run("manage session with management and public URL", func(t *testing.T) {
		_, got := session(t, auth.RoleAdminManage, managed, true)
		want := protocol.AdminSession{Authenticated: true, Role: "admin-manage", CredentialManagement: true, PublicURL: "https://dumb.example"}
		if got != want {
			t.Fatalf("got %+v want %+v", got, want)
		}
	})
	t.Run("read-only session sees its role", func(t *testing.T) {
		_, got := session(t, auth.RoleAdminRead, managed, true)
		if got.Role != "admin-read" || !got.CredentialManagement {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("management not configured", func(t *testing.T) {
		body, got := session(t, auth.RoleAdminManage, nil, true)
		if got != (protocol.AdminSession{Authenticated: true, Role: "admin-manage"}) || strings.Contains(body, "credential_management") || strings.Contains(body, "public_url") {
			t.Fatalf("body %s", body)
		}
	})
	t.Run("worker role is not an admin session", func(t *testing.T) {
		body, _ := session(t, auth.RoleWorker, managed, true)
		if body != `{"authenticated":false}` {
			t.Fatalf("worker body %s", body)
		}
	})
}

// Without management the pairing endpoint is refused by the origin check before
// it can say 503; the capability flag is what lets the panel explain it.
func TestAdminPairingWithoutManagementIsRefused(t *testing.T) {
	e := newEnvWith(t, func(c *Config) { c.AdminSessions = &fixtureAdminSessions{role: auth.RoleAdminManage} })
	r := httptest.NewRequest("POST", "https://admin.invalid/admin/v1/pairing", strings.NewReader(`{"ttl_seconds":600,"credential_ttl_seconds":31536000}`))
	r.Header.Set("Origin", "https://admin.invalid")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "dumb_admin_session", Value: randomAdminToken()})
	w := httptest.NewRecorder()
	e.srv.AdminHandler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("pairing without management: %d", w.Code)
	}
}

func TestAdminPublicURLValidation(t *testing.T) {
	good := map[string]string{
		"https://dumb.example":           "https://dumb.example",
		"https://dumb.example/":          "https://dumb.example",
		"https://dumb.example:8443/api/": "https://dumb.example:8443/api",
	}
	for in, want := range good {
		if got, err := NormalizePublicURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "http://dumb.example", "dumb.example", "https://", "https:///path", "https://u:p@dumb.example", "https://dumb.example/?q=1", "https://dumb.example/?", "https://dumb.example/#x", "https://dumb.example/a b", "https://dumb.example/'x", "https://dumb.example/\"x", "https://dumb.example/<x>", "javascript:alert(1)", "https://" + strings.Repeat("a", 2050)} {
		if got, err := NormalizePublicURL(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
	tokens := auth.NewStaticTokens()
	if _, err := New(Config{Auth: tokens, MinWorkerVersion: "0.0.0", PublicURL: "http://dumb.example"}); err == nil {
		t.Fatal("New accepted an http public URL")
	}
	s, err := New(Config{Auth: tokens, MinWorkerVersion: "0.0.0", PublicURL: "https://dumb.example/"})
	if err != nil || s.cfg.PublicURL != "https://dumb.example" {
		t.Fatalf("New: %v %q", err, s.cfg.PublicURL)
	}
	s.Close()
}
