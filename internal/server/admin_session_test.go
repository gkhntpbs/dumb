package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/auth"
)

type fixtureAdminSessions struct {
	role  auth.Role
	calls int
}

func (f *fixtureAdminSessions) Authenticate(r *http.Request) (auth.Principal, error) {
	if _, e := r.Cookie("dumb_admin_session"); e != nil {
		return auth.Principal{}, auth.ErrUnauthorized
	}
	return auth.Principal{ID: "fixture-admin", Role: f.role}, nil
}
func (f *fixtureAdminSessions) Login(w http.ResponseWriter, r *http.Request, password string) error {
	f.calls++
	// Test-only credential supplied from the request, never a stored password.
	if password == "" {
		return auth.ErrUnauthorized
	}
	http.SetCookie(w, &http.Cookie{Name: "dumb_admin_session", Value: randomAdminToken(), HttpOnly: true, SameSite: http.SameSiteStrictMode, Path: "/"})
	return nil
}
func (f *fixtureAdminSessions) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "dumb_admin_session", MaxAge: -1, Path: "/"})
}

func TestAdminBrowserSessionBoundary(t *testing.T) {
	s, _, bearer := adminFixture(t)
	provider := &fixtureAdminSessions{role: auth.RoleAdminRead}
	s.cfg.AdminSessions = provider
	h := s.AdminHandler()
	req := func(method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://admin.invalid"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+bearer)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cached response")
		}
		return w
	}
	if w := req("GET", "/admin/v1/session", "", nil, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal(w.Body.String())
	}
	if w := req("GET", "/admin/v1/summary", "", nil, ""); w.Code != 401 {
		t.Fatal("bearer bypassed browser provider", w.Code)
	}
	body := `{"password":"` + randomAdminToken() + `"}`
	for _, origin := range []string{"", "null", "https://attacker.invalid", "http://admin.invalid.attacker.invalid"} {
		if w := req("POST", "/admin/v1/login", body, nil, origin); w.Code != 403 {
			t.Fatal(origin, w.Code)
		}
	}
	if provider.calls != 0 {
		t.Fatal("cross-origin password processed")
	}
	for _, bad := range []string{`{}`, `{"password":1}`, body + ` {}`, `{"password":"value","extra":true}`} {
		if w := req("POST", "/admin/v1/login", bad, nil, "http://admin.invalid"); w.Code != 400 {
			t.Fatal(bad, w.Code)
		}
	}
	rType := httptest.NewRequest("POST", "http://admin.invalid/admin/v1/login", strings.NewReader(body))
	rType.Header.Set("Origin", "http://admin.invalid")
	rType.Header.Set("Content-Type", "text/plain")
	wType := httptest.NewRecorder()
	h.ServeHTTP(wType, rType)
	if wType.Code != 415 {
		t.Fatal("non-JSON accepted", wType.Code)
	}
	if w := req("POST", "/admin/v1/login", `{"password":"`+strings.Repeat("x", 513)+`"}`, nil, "http://admin.invalid"); w.Code != 400 {
		t.Fatal("oversized password", w.Code)
	}

	w := req("POST", "/admin/v1/login", body, nil, "http://admin.invalid")
	if w.Code != 204 || len(w.Result().Cookies()) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if w := req("GET", "/admin/v1/session", "", cookie, ""); !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"summary", "workers", "queue"} {
		if w := req("GET", "/admin/v1/"+path, "", cookie, ""); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	provider.role = auth.RoleCaller
	if w := req("GET", "/admin/v1/summary", "", cookie, ""); w.Code != 401 {
		t.Fatal("role bypass", w.Code)
	}
	if w := req("GET", "/admin/v1/session", "", cookie, ""); !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal("wrong role session")
	}
	if w := req("POST", "/admin/v1/logout", "", cookie, "http://admin.invalid"); w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout")
	}
	r := httptest.NewRequest("POST", "http://admin.invalid/admin/v1/login", strings.NewReader(body))
	r.Header.Set("Origin", "https://attacker.invalid")
	r.Header.Set("X-Forwarded-Host", "attacker.invalid")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("forwarded origin bypass")
	}
	r = httptest.NewRequest("POST", "http://admin.invalid/admin/v1/login", strings.NewReader(body))
	r.Header.Set("Origin", "http://admin.invalid")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("fetch metadata bypass")
	}
}

func TestAdminLoginRateLimitUsesPeerAddress(t *testing.T) {
	s, _, _ := adminFixture(t)
	provider := &fixtureAdminSessions{role: auth.RoleAdminRead}
	s.cfg.AdminSessions = provider
	h := s.AdminHandler()
	body := `{"password":"` + randomAdminToken() + `"}`
	limited := false
	for i := 0; i < 100; i++ {
		r := httptest.NewRequest("POST", "http://admin.invalid/admin/v1/login", strings.NewReader(body))
		r.Header.Set("Origin", "http://admin.invalid")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", randomAdminToken())
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 429 {
			limited = true
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("missing retry time")
			}
			break
		}
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if !limited {
		t.Fatal("forwarded addresses bypassed login limit")
	}
}

// observingAdminSessions also implements AdminOriginRejectionObserver.
type observingAdminSessions struct {
	fixtureAdminSessions
	rejected []string
}

func (o *observingAdminSessions) RejectedOrigin(r *http.Request) {
	o.rejected = append(o.rejected, r.Method+" "+r.URL.Path)
}

// Cross-origin admin requests are refused before the provider sees them; a
// provider that observes refusals is told exactly once per refusal, and the
// request never reaches Login.
func TestAdminCrossOriginRefusalIsObserved(t *testing.T) {
	s, _, _ := adminFixture(t)
	provider := &observingAdminSessions{fixtureAdminSessions: fixtureAdminSessions{role: auth.RoleAdminManage}}
	s.cfg.AdminSessions = provider
	s.cfg.AdminOrigin = "http://admin.invalid"
	h := s.AdminHandler()
	for _, tc := range []struct{ method, path, origin, site string }{
		{"POST", "/admin/v1/login", "https://evil.invalid", ""},
		{"POST", "/admin/v1/login", "", ""},
		{"POST", "/admin/v1/login", "http://admin.invalid", "cross-site"},
		{"POST", "/admin/v1/logout", "https://evil.invalid", ""},
		{"GET", "/admin/v1/summary", "https://evil.invalid", ""},
		{"GET", "/admin/v1/credentials", "https://evil.invalid", ""},
	} {
		provider.rejected = nil
		r := httptest.NewRequest(tc.method, "http://admin.invalid"+tc.path, strings.NewReader(`{"password":"fixture"}`))
		r.Header.Set("Content-Type", "application/json")
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%+v: status %d, want 403", tc, w.Code)
		}
		if want := tc.method + " " + tc.path; len(provider.rejected) != 1 || provider.rejected[0] != want {
			t.Fatalf("%+v: observed %v, want exactly [%s]", tc, provider.rejected, want)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("refused login reached the provider %d times", provider.calls)
	}
	// Same-origin requests are not reported.
	provider.rejected = nil
	r := httptest.NewRequest("GET", "http://admin.invalid/admin/v1/session", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if len(provider.rejected) != 0 {
		t.Fatalf("same-origin request reported: %v", provider.rejected)
	}
}

// A provider without the optional observer keeps the plain 403.
func TestAdminCrossOriginRefusalWithoutObserver(t *testing.T) {
	s, _, _ := adminFixture(t)
	s.cfg.AdminSessions = &fixtureAdminSessions{role: auth.RoleAdminRead}
	r := httptest.NewRequest("POST", "http://admin.invalid/admin/v1/login", strings.NewReader(`{"password":"fixture"}`))
	r.Header.Set("Origin", "https://evil.invalid")
	w := httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
}
