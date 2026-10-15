package doctor

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/server"
)

type fakeOllama struct {
	err    error // every call fails with it when set
	models []string
	out    string
}

func (f *fakeOllama) Version(context.Context) (string, error) { return "0.35.0", f.err }
func (f *fakeOllama) List(context.Context) ([]string, error)  { return f.models, f.err }
func (f *fakeOllama) Generate(context.Context, ollama.GenerateRequest) (string, error) {
	return f.out, f.err
}

func ipnet(s string) net.Addr { return &net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(24, 32)} }

// healthy returns a Doctor where every check passes, against a real dumb-server.
func healthy(t *testing.T) *Doctor {
	t.Helper()
	tokens := auth.NewStaticTokens()
	tokens.Add("w1", auth.RoleWorker, "good-token")
	s, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return &Doctor{
		ServerURL: ts.URL, Token: "good-token", Version: "1.2.0", Models: []string{"smollm2:135m"},
		Ollama: &fakeOllama{models: []string{"smollm2:135m"}, out: "ok"},
		Detect: func(context.Context) ollama.Detection {
			return ollama.Detection{Path: "/Applications/Ollama.app", Installed: true, Reachable: true, Version: "0.35.0"}
		},
		HTTP:           &http.Client{Timeout: 5 * time.Second},
		Getenv:         func(string) string { return "" },
		InterfaceAddrs: func() ([]net.Addr, error) { return []net.Addr{ipnet("127.0.0.1"), ipnet("192.0.2.10")}, nil },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("connection refused")
		},
	}
}

func find(t *testing.T, rs []Result, name string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no check %q in %+v", name, rs)
	return Result{}
}

func TestAllPass(t *testing.T) {
	rs := healthy(t).Run(context.Background())
	var buf bytes.Buffer
	if !Print(&buf, rs) {
		t.Fatalf("not all passed:\n%s", buf.String())
	}
	if len(rs) != 7 || !strings.Contains(buf.String(), "[ok  ] Credential and version: accepted as worker w1") {
		t.Errorf("output:\n%s", buf.String())
	}
}

// TestEachCheckFails breaks one thing at a time and expects exactly that check to fail visibly.
func TestEachCheckFails(t *testing.T) {
	cases := []struct {
		check  string
		break_ func(d *Doctor)
		detail string
	}{
		{"Ollama installed", func(d *Doctor) {
			d.Detect = func(context.Context) ollama.Detection { return ollama.Detection{} }
		}, "dumb-worker setup"},
		{"Ollama reachable", func(d *Doctor) {
			d.Ollama = &fakeOllama{err: errors.New("connection refused")}
		}, "connection refused"},
		{"Ollama not exposed", func(d *Doctor) {
			d.Getenv = func(k string) string { return map[string]string{"OLLAMA_HOST": "0.0.0.0:11434"}[k] }
		}, "OLLAMA_HOST=0.0.0.0:11434"},
		{"Ollama not exposed", func(d *Doctor) {
			d.Dial = func(_ context.Context, _, addr string) (net.Conn, error) {
				if addr != "192.0.2.10:11434" {
					t.Errorf("dialled %s", addr)
				}
				c, s := net.Pipe()
				s.Close()
				return c, nil
			}
		}, "reachable from the network at 192.0.2.10:11434"},
		{"Model smollm2:135m", func(d *Doctor) {
			d.Ollama = &fakeOllama{models: []string{"other:1b"}, out: "ok"}
		}, "missing"},
		{"Server reachable", func(d *Doctor) { d.ServerURL = "http://127.0.0.1:1" }, "refused"},
		{"Credential and version", func(d *Doctor) { d.Token = "wrong" }, "credential rejected"},
		{"Credential and version", func(d *Doctor) { d.Version = "0.9.0" }, "worker too old, minimum 1.0.0"},
		{"Sample latency", func(d *Doctor) {
			d.Ollama = &fakeOllama{models: []string{"smollm2:135m"}}
		}, "empty response"},
	}
	for _, c := range cases {
		d := healthy(t)
		c.break_(d)
		rs := d.Run(context.Background())
		r := find(t, rs, c.check)
		var buf bytes.Buffer
		if Print(&buf, rs) || r.OK || !strings.Contains(r.Detail, c.detail) {
			t.Errorf("%s: %+v, want failure containing %q", c.check, r, c.detail)
		}
		if !strings.Contains(buf.String(), "[FAIL] "+c.check) {
			t.Errorf("%s: not printed as FAIL:\n%s", c.check, buf.String())
		}
	}
}

func TestOllamaHostSetting(t *testing.T) {
	for v, want := range map[string]bool{
		"127.0.0.1": true, "127.0.0.1:11434": true, "http://localhost:11434": true, "[::1]:11434": true,
		"0.0.0.0": false, ":11434": false, "http://0.0.0.0:11434": false, "192.168.1.2": false,
	} {
		if got := loopbackHostSetting(v); got != want {
			t.Errorf("%q: got %v", v, got)
		}
	}
}

// In remote mode the local install and exposure checks do not apply and the
// model hint never suggests a local pull.
func TestRemoteMode(t *testing.T) {
	d := healthy(t)
	d.Remote = true
	d.Detect = func(context.Context) ollama.Detection {
		t.Error("remote mode looked for a local Ollama")
		return ollama.Detection{}
	}
	d.Getenv = func(string) string { return "0.0.0.0" } // would fail the local exposure check
	d.Dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("remote mode probed local interfaces")
		return nil, errors.New("refused")
	}
	rs := d.Run(context.Background())
	var buf bytes.Buffer
	if !Print(&buf, rs) || !strings.Contains(buf.String(), "private-network host (remote mode)") {
		t.Fatalf("remote mode:\n%s", buf.String())
	}
	d.Models = []string{"absent:1b"}
	r := find(t, d.Run(context.Background()), "Model absent:1b")
	if r.OK || !strings.Contains(r.Detail, "on that host") || strings.Contains(r.Detail, "dumb-worker setup") {
		t.Fatalf("missing model: %+v", r)
	}
}
