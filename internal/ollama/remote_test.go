package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIsPrivateAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"10.0.0.1": true, "10.255.255.254": true, "172.16.0.1": true, "172.31.255.254": true,
		"192.168.10.20": true, "fd00::1": true, "fc00::1": true, "fd7a:115c:a1e0::1": true,
		"::ffff:192.168.1.5": true,
		"172.15.255.255":     false, "172.32.0.1": false, "192.169.0.1": false, "11.0.0.1": false,
		"127.0.0.1": false, "127.1.2.3": false, "::1": false, "::ffff:127.0.0.1": false,
		"169.254.1.1": false, "fe80::1": false, "0.0.0.0": false, "::": false,
		"224.0.0.1": false, "239.255.255.250": false, "ff02::1": false, "255.255.255.255": false,
		"8.8.8.8": false, "100.64.0.1": false, "2001:db8::1": false, "2a00:1450::1": false,
		"::ffff:8.8.8.8": false, "fec0::1": false,
	} {
		if got := IsPrivateAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("IsPrivateAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

// Without the opt-in nothing changes: loopback only.
func TestRemoteRequiresOptIn(t *testing.T) {
	if _, err := Open("http://192.168.10.20:11434", false); err == nil {
		t.Fatal("a private-network URL was accepted without the remote opt-in")
	}
	c, err := Open("http://127.0.0.1:11434", false)
	if err != nil || c.Remote() {
		t.Fatalf("loopback without opt-in: %v, remote %v", err, c != nil && c.Remote())
	}
	c, err = Open("http://192.168.10.20:11434", true)
	if err != nil || !c.Remote() {
		t.Fatalf("private with opt-in: %v", err)
	}
}

func TestRemoteRejectsNonPrivateURLs(t *testing.T) {
	for _, u := range []string{
		"http://8.8.8.8:11434", "http://[2001:db8::1]:11434", "http://100.64.0.1:11434",
		"http://127.0.0.1:11434", "http://localhost:11434", "http://LOCALHOST:11434", "http://ollama.localhost:11434",
		"http://[::1]:11434", "http://[::ffff:127.0.0.1]:11434", "http://[::ffff:8.8.8.8]:11434",
		"http://169.254.169.254", "http://[fe80::1%25eth0]:11434", "http://0.0.0.0:11434", "http://[::]:11434",
		"http://224.0.0.1:11434", "http://user:pass@192.168.1.5:11434", "ftp://192.168.1.5", "http://:11434",
	} {
		if _, err := Open(u, true); err == nil {
			t.Errorf("%s accepted in remote mode", u)
		}
	}
}

// fakeResolver answers per host; calls are counted so a rebinding answer can follow.
type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][][]string // successive answers per host
	calls   map[string]int
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := r.answers[host]
	if len(seq) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	i := min(r.calls[host], len(seq)-1)
	r.calls[host]++
	var out []netip.Addr
	for _, a := range seq[i] {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

// remoteFixture is a fake Ollama reached through a pinned dial: whatever
// validated address the client dials, the connection lands on the fixture.
type remoteFixture struct {
	srv    *httptest.Server
	mu     sync.Mutex
	dialed []string
	paths  []string
}

func newRemoteFixture(t *testing.T, models ...string) *remoteFixture {
	t.Helper()
	f := &remoteFixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		switch r.URL.Path {
		case "/api/version":
			json.NewEncoder(w).Encode(map[string]string{"version": "0.35.0"})
		case "/api/tags":
			var list []map[string]string
			for _, m := range models {
				list = append(list, map[string]string{"name": m})
			}
			json.NewEncoder(w).Encode(map[string]any{"models": list})
		case "/api/generate":
			json.NewEncoder(w).Encode(map[string]string{"response": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *remoteFixture) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.dialed = append(f.dialed, addr)
	f.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, f.srv.Listener.Addr().String())
}

func (f *remoteFixture) client(t *testing.T, url string, r Resolver) *Client {
	t.Helper()
	c, err := openRemote(url, r, f.dial)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRemoteDialPinsValidatedAddress(t *testing.T) {
	f := newRemoteFixture(t, "m:1b")
	r := &fakeResolver{answers: map[string][][]string{"mini.lan": {{"192.168.10.20"}}}, calls: map[string]int{}}
	t.Setenv("HTTP_PROXY", "http://8.8.8.8:3128") // never consulted in remote mode
	c := f.client(t, "http://mini.lan:11434", r)
	if v, err := c.Version(context.Background()); err != nil || v != "0.35.0" {
		t.Fatalf("version %q, %v", v, err)
	}
	if strings.Join(f.dialed, ",") != "192.168.10.20:11434" {
		t.Fatalf("dialed %q, want the validated IP only", f.dialed)
	}
}

func TestRemoteRefusesPublicOrMixedResolution(t *testing.T) {
	for name, answer := range map[string][]string{
		"public":    {"93.184.216.34"},
		"mixed":     {"192.168.10.20", "93.184.216.34"},
		"loopback":  {"127.0.0.1"},
		"linklocal": {"169.254.169.254"},
		"mapped":    {"::ffff:8.8.8.8"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t, "m:1b")
			r := &fakeResolver{answers: map[string][][]string{"mini.lan": {answer}}, calls: map[string]int{}}
			c := f.client(t, "http://mini.lan:11434", r)
			_, err := c.Version(context.Background())
			if !errors.Is(err, ErrNotPrivate) || len(f.dialed) != 0 {
				t.Fatalf("err %v, dialed %q; want ErrNotPrivate and no connection", err, f.dialed)
			}
		})
	}
}

// DNS rebinding: a name that was private at first and public later is
// refused on the next connection; nothing is ever dialed at the public address.
func TestRemoteRebindingRefused(t *testing.T) {
	f := newRemoteFixture(t, "m:1b")
	r := &fakeResolver{answers: map[string][][]string{"mini.lan": {{"192.168.10.20"}, {"203.0.113.9"}}}, calls: map[string]int{}}
	c := f.client(t, "http://mini.lan:11434", r)
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.http.Transport.(*http.Transport).CloseIdleConnections() // force a new connection
	if _, err := c.Version(context.Background()); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("after rebinding: %v, want ErrNotPrivate", err)
	}
	for _, a := range f.dialed {
		if !strings.HasPrefix(a, "192.168.10.20:") {
			t.Fatalf("dialed %s", a)
		}
	}
}

func TestRemoteIPLiteralStillCheckedAtDial(t *testing.T) {
	f := newRemoteFixture(t)
	c := f.client(t, "http://10.1.2.3:11434", &fakeResolver{calls: map[string]int{}})
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.dialed, ",") != "10.1.2.3:11434" {
		t.Fatalf("dialed %q", f.dialed)
	}
}

// remoteSystem is a System whose every management action fails the test.
type forbiddenRunner struct{ t *testing.T }

func (r forbiddenRunner) Run(_ context.Context, name string, args ...string) error {
	r.t.Errorf("remote mode ran %s %v", name, args)
	return errors.New("forbidden")
}
func (r forbiddenRunner) Start(name string, args ...string) error {
	r.t.Errorf("remote mode started %s %v", name, args)
	return errors.New("forbidden")
}

func remoteRecovery(t *testing.T, c *Client, models ...string) *Recovery {
	t.Helper()
	s := NewSystem(c)
	s.GOOS = "linux"
	s.Runner = forbiddenRunner{t}
	s.LookPath = func(string) (string, error) {
		t.Error("remote mode looked for a local Ollama")
		return "", errors.New("no")
	}
	rec, err := NewRecovery(RecoveryConfig{System: s, Models: models, Backoff: time.Millisecond, Cooldown: time.Millisecond, MaxCooldown: time.Millisecond, ProbeTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestRemoteRecoveryOnlyChecks(t *testing.T) {
	r := &fakeResolver{answers: map[string][][]string{"mini.lan": {{"192.168.10.20"}}}, calls: map[string]int{}}

	f := newRemoteFixture(t, "m:1b")
	if err := remoteRecovery(t, f.client(t, "http://mini.lan:11434", r), "m:1b").Ensure(context.Background()); err != nil {
		t.Fatalf("present model: %v", err)
	}

	f = newRemoteFixture(t, "other:1b")
	err := remoteRecovery(t, f.client(t, "http://mini.lan:11434", r), "m:1b").Ensure(context.Background())
	if !errors.Is(err, ErrRemoteModelMissing) {
		t.Fatalf("missing model: %v, want ErrRemoteModelMissing", err)
	}
	for _, p := range f.paths {
		if p == "/api/pull" {
			t.Fatal("remote mode pulled a model")
		}
	}

	f = newRemoteFixture(t)
	f.srv.Close() // unreachable
	err = remoteRecovery(t, f.client(t, "http://mini.lan:11434", r), "m:1b").Ensure(context.Background())
	if !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("unreachable: %v, want ErrRemoteUnavailable", err)
	}
}

func TestRemoteSystemRefusesManagement(t *testing.T) {
	c, err := Open("http://192.168.10.20:11434", true)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSystem(c)
	s.Runner = forbiddenRunner{t}
	if err := s.Install(context.Background()); !errors.Is(err, ErrRemoteManaged) {
		t.Errorf("Install: %v", err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, ErrRemoteManaged) {
		t.Errorf("Start: %v", err)
	}
	if err := c.Pull(context.Background(), "m", nil); !errors.Is(err, ErrRemoteManaged) {
		t.Errorf("Pull: %v", err)
	}
}
