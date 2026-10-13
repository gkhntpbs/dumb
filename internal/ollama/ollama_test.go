package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeOllama serves the subset of the Ollama API the client uses.
func fakeOllama(t *testing.T, pullLines []string) (*httptest.Server, *Client) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"0.35.0"}`)
	})
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"models":[{"name":"smollm2:135m"},{"name":"llama3:latest"}]}`)
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, _ *http.Request) {
		for _, l := range pullLines {
			fmt.Fprintln(w, l)
		}
	})
	mux.HandleFunc("POST /api/generate", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["model"] == "missing" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"model 'missing' not found"}`)
			return
		}
		if req["stream"] != false || req["format"] != "json" || req["system"] != "be brief" {
			t.Errorf("generate request: %v", req)
		}
		json.NewEncoder(w).Encode(map[string]any{"response": "echo:" + req["prompt"].(string), "done": true})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	return ts, c
}

func TestLoopbackOnly(t *testing.T) {
	for _, ok := range []string{"", "http://127.0.0.1:11434", "http://localhost:1", "http://[::1]:11434", "http://127.0.0.2"} {
		if _, err := New(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://0.0.0.0:11434", "http://192.168.1.5:11434", "http://example.invalid", "ftp://127.0.0.1", "http://127.0.0.1.example.invalid"} {
		if _, err := New(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestVersionListGenerate(t *testing.T) {
	_, c := fakeOllama(t, nil)
	ctx := context.Background()
	if v, err := c.Version(ctx); err != nil || v != "0.35.0" {
		t.Fatalf("version: %q %v", v, err)
	}
	names, err := c.List(ctx)
	if err != nil || !slices.Equal(names, []string{"smollm2:135m", "llama3:latest"}) {
		t.Fatalf("list: %v %v", names, err)
	}
	if !HasModel(names, "llama3") || !HasModel(names, "smollm2:135m") || HasModel(names, "smollm2") {
		t.Error("HasModel")
	}
	out, err := c.Generate(ctx, GenerateRequest{Model: "m", Prompt: "hi", System: "be brief", Format: "json"})
	if err != nil || out != "echo:hi" {
		t.Fatalf("generate: %q %v", out, err)
	}
	if _, err := c.Generate(ctx, GenerateRequest{Model: "missing", Prompt: "hi"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("generate missing: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Generate(cctx, GenerateRequest{Model: "m", Prompt: "hi"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled generate: %v", err)
	}
}

func TestPull(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		err   string
		seen  int
	}{
		{"success", []string{`{"status":"pulling manifest"}`, `{"status":"pulling abc","digest":"abc","total":10,"completed":5}`, ``, `{"status":"success"}`}, "", 3},
		{"error line", []string{`{"status":"pulling manifest"}`, `{"error":"pull model manifest: file does not exist"}`}, "file does not exist", 1},
		{"no success", []string{`{"status":"pulling manifest"}`}, "without success", 1},
	}
	for _, c := range cases {
		_, cl := fakeOllama(t, c.lines)
		var seen []Progress
		err := cl.Pull(context.Background(), "m", func(p Progress) { seen = append(seen, p) })
		if (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		}
		if len(seen) != c.seen {
			t.Errorf("%s: %d progress lines, want %d", c.name, len(seen), c.seen)
		}
	}
}

type fakeRunner struct {
	runs, starts []string
	fail         map[string]bool // command name -> Run fails
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.runs = append(f.runs, strings.Join(append([]string{name}, args...), " "))
	if f.fail[name] {
		return errors.New(name + " failed")
	}
	return nil
}

func (f *fakeRunner) Start(name string, args ...string) error {
	f.starts = append(f.starts, strings.Join(append([]string{name}, args...), " "))
	return nil
}

// fakeSystem has no binary on PATH and only the given files present.
func fakeSystem(goos string, files []string, c *Client) (*System, *fakeRunner, *[]string) {
	r := &fakeRunner{fail: map[string]bool{}}
	var downloads []string
	return &System{
		GOOS:         goos,
		Home:         "/home/u",
		LocalAppData: `C:\Users\u\AppData\Local`,
		LookPath:     func(string) (string, error) { return "", errors.New("not found") },
		Stat: func(p string) (fs.FileInfo, error) {
			if slices.ContainsFunc(files, func(file string) bool { return filepath.Clean(file) == filepath.Clean(p) }) {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		},
		Writable: func(string) bool { return false },
		Download: func(_ context.Context, url, dst string) error {
			downloads = append(downloads, url+" -> "+dst)
			return nil
		},
		TempDir: func() (string, error) { return "/tmp/x", nil },
		Runner:  r,
		Client:  c,
	}, r, &downloads
}

func TestDetect(t *testing.T) {
	_, up := fakeOllama(t, nil)
	down, _ := New("http://127.0.0.1:1") // nothing listens on port 1
	cases := []struct {
		name      string
		goos      string
		files     []string
		client    *Client
		path      string
		reachable bool
	}{
		{"absent", "darwin", nil, down, "", false},
		{"app present, stopped", "darwin", []string{"/home/u/Applications/Ollama.app"}, down, "/home/u/Applications/Ollama.app", false},
		{"running, no binary found", "linux", nil, up, "", true},
		{"linux binary, running", "linux", []string{"/usr/bin/ollama"}, up, "/usr/bin/ollama", true},
	}
	for _, c := range cases {
		s, _, _ := fakeSystem(c.goos, c.files, c.client)
		d := s.Detect(context.Background())
		if filepath.Clean(d.Path) != filepath.Clean(c.path) || d.Reachable != c.reachable || d.Installed != (c.path != "" || c.reachable) {
			t.Errorf("%s: %+v", c.name, d)
		}
		if c.reachable && d.Version != "0.35.0" {
			t.Errorf("%s: version %q", c.name, d.Version)
		}
	}
	s, _, _ := fakeSystem("windows", nil, down)
	s.LookPath = func(string) (string, error) { return `C:\bin\ollama.exe`, nil }
	if d := s.Detect(context.Background()); d.Path != `C:\bin\ollama.exe` || !d.Installed {
		t.Errorf("PATH lookup: %+v", d)
	}
}

func TestInstallPerOS(t *testing.T) {
	cases := []struct {
		goos      string
		runs      []string
		downloads []string
	}{
		{"linux", []string{"sh -c " + LinuxInstallScript}, nil},
	}
	for _, c := range cases {
		s, r, dl := fakeSystem(c.goos, nil, nil)
		if err := s.Install(context.Background()); err != nil {
			t.Fatalf("%s: %v", c.goos, err)
		}
		if !slices.Equal(r.runs, c.runs) || !slices.Equal(*dl, c.downloads) {
			t.Errorf("%s: runs %q downloads %q", c.goos, r.runs, *dl)
		}
	}
	s, _, _ := fakeSystem("darwin", nil, nil)
	if err := s.Install(context.Background()); err == nil {
		t.Fatal("macOS install accepted missing filesystem seam")
	}
	if s, _, _ := fakeSystem("plan9", nil, nil); s.Install(context.Background()) == nil {
		t.Error("unsupported OS installed")
	}
}

func TestStart(t *testing.T) {
	down, _ := New("http://127.0.0.1:1")
	_, up := fakeOllama(t, nil)

	s, r, _ := fakeSystem("darwin", []string{"/Applications/Ollama.app", "/Applications/Ollama.app/Contents/Resources/ollama"}, down)
	if err := s.Start(context.Background()); err != nil || !slices.Equal(r.starts, []string{filepath.Join("/Applications/Ollama.app", "Contents", "Resources", "ollama") + " serve"}) || len(r.runs) != 0 {
		t.Errorf("darwin: %v %q", err, r.runs)
	}
	s, r, _ = fakeSystem("linux", []string{"/usr/bin/ollama"}, down)
	r.fail["systemctl"] = true
	if err := s.Start(context.Background()); err != nil || !slices.Equal(r.starts, []string{"/usr/bin/ollama serve"}) {
		t.Errorf("linux fallback: %v %q %q", err, r.runs, r.starts)
	}
	s, r, _ = fakeSystem("linux", []string{"/usr/bin/ollama"}, down)
	if s.Start(context.Background()); !slices.Equal(r.runs, []string{"systemctl start ollama"}) || r.starts != nil {
		t.Errorf("linux systemd: %q %q", r.runs, r.starts)
	}
	s, r, _ = fakeSystem("linux", nil, up)
	if err := s.Start(context.Background()); err != nil || r.runs != nil || r.starts != nil {
		t.Errorf("already running: %v %q", err, r.runs)
	}
	s, _, _ = fakeSystem("linux", nil, down)
	if s.Start(context.Background()) == nil {
		t.Error("started a missing install")
	}
}
