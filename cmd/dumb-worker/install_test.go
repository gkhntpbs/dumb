//go:build !windows

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

type recordRunner struct {
	calls []string
	fail  map[string]error // by joined command line
}

func (r *recordRunner) Run(_ context.Context, name string, args ...string) error {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, line)
	return r.fail[line]
}

type installFixture struct {
	t        *testing.T
	in       *installer
	runner   *recordRunner
	out      *bytes.Buffer
	home     string
	root     string
	pairs    []string // "server code" per exchange
	chowns   map[string][2]int
	setups   int
	setupErr error
	users    map[string][2]int
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &installFixture{t: t, runner: &recordRunner{fail: map[string]error{}}, out: &bytes.Buffer{},
		home: filepath.Join(base, "home"), root: filepath.Join(base, "root"), chowns: map[string][2]int{}, users: map[string][2]int{}}
	for _, d := range []string{f.home, f.root} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(base, "dumb-worker-download")
	if err := os.WriteFile(exe, []byte("binary v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.in = &installer{
		goos: "linux", euid: 1000, home: f.home, username: "pi", root: f.root, exe: exe,
		stdout: f.out, log: log.New(io.Discard, "", 0), getenv: func(string) string { return "" },
		runner: f.runner,
		lookupUser: func(name string) (int, int, error) {
			if id, ok := f.users[name]; ok {
				return id[0], id[1], nil
			}
			return 0, 0, user.UnknownUserError(name)
		},
		chown: func(p string, uid, gid int) error {
			// Files are chowned while staged, before the rename: record the target.
			if strings.HasPrefix(filepath.Base(p), ".dumb-install-") {
				p = filepath.Join(filepath.Dir(p), "<staged>")
			}
			f.chowns[p] = [2]int{uid, gid}
			return nil
		},
		pair: func(_ context.Context, server, code string) (string, error) {
			f.pairs = append(f.pairs, server+" "+code)
			return "paired-token-" + code, nil
		},
		setup:  func(context.Context, *config) error { f.setups++; return f.setupErr },
		linger: func(string) bool { return false },
	}
	return f
}

func (f *installFixture) opts(args ...string) *installOptions {
	f.t.Helper()
	o, err := parseInstallFlags(args, io.Discard)
	if err != nil {
		f.t.Fatal(err)
	}
	return o
}

func (f *installFixture) install(args ...string) error {
	f.t.Helper()
	f.runner.calls = nil
	f.out.Reset()
	return f.in.install(context.Background(), f.opts(args...))
}

func (f *installFixture) tokenFile(content string) string {
	f.t.Helper()
	p := filepath.Join(f.home, "issued-token")
	if err := os.WriteFile(p, []byte(content+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallUserFreshAndIdempotent(t *testing.T) {
	f := newInstallFixture(t)
	args := []string{"-server", "https://dumb.example", "-models", "smollm2:135m", "-token-file", f.tokenFile("issued")}
	if err := f.install(args...); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.home, ".local", "share", "dumb-worker")
	bin, token := filepath.Join(dir, "bin", "dumb-worker"), filepath.Join(dir, "token")
	unit := filepath.Join(f.home, ".config", "systemd", "user", "dumb-worker.service")
	if read(t, bin) != "binary v1" || mode(t, bin) != 0o755 {
		t.Errorf("binary %q mode %v", read(t, bin), mode(t, bin))
	}
	if read(t, token) != "issued\n" || mode(t, token) != 0o600 || mode(t, dir) != 0o700 {
		t.Errorf("token %q mode %v, dir mode %v", read(t, token), mode(t, token), mode(t, dir))
	}
	u := read(t, unit)
	for _, want := range []string{`ExecStart="` + bin + `" run`, `"DUMB_TOKEN_FILE=` + token + `"`, `"DUMB_HEADLESS=1"`,
		`"DUMB_SERVER=https://dumb.example"`, `"DUMB_MODELS=smollm2:135m"`, "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %s:\n%s", want, u)
		}
	}
	if strings.Contains(u, "issued") {
		t.Error("the unit contains the token")
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable --now dumb-worker.service", "systemctl --user is-active --quiet dumb-worker.service"}
	if strings.Join(f.runner.calls, "|") != strings.Join(want, "|") {
		t.Errorf("commands %q, want %q", f.runner.calls, want)
	}
	if f.setups != 1 || !strings.Contains(f.out.String(), "installed") || !strings.Contains(f.out.String(), "enable-linger") {
		t.Errorf("setups %d, output %q", f.setups, f.out.String())
	}
	before, _ := os.Stat(bin)

	// The same command again changes nothing and restarts nothing.
	if err := f.install(args...); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(bin); !os.SameFile(before, after) {
		t.Error("an identical binary was rewritten")
	}
	for _, c := range f.runner.calls {
		if strings.Contains(c, "restart") {
			t.Errorf("unchanged re-run restarted the service: %q", f.runner.calls)
		}
	}
	if !strings.Contains(f.out.String(), "unchanged") {
		t.Errorf("output %q, want unchanged", f.out.String())
	}

	// A changed configuration rewrites the unit and restarts the service.
	args[3] = "smollm2:135m,qwen3:0.6b"
	if err := f.install(args...); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.runner.calls, "|"), "systemctl --user restart dumb-worker.service") || !strings.Contains(read(t, unit), "qwen3:0.6b") {
		t.Errorf("changed config: commands %q", f.runner.calls)
	}
	// So does a new binary.
	os.WriteFile(f.in.exe, []byte("binary v2"), 0o755)
	if err := f.install(args...); err != nil {
		t.Fatal(err)
	}
	if read(t, bin) != "binary v2" || !strings.Contains(strings.Join(f.runner.calls, "|"), "restart") {
		t.Errorf("new binary: %q, commands %q", read(t, bin), f.runner.calls)
	}
}

func TestInstallPairingSources(t *testing.T) {
	code := strings.Repeat("c", 43)
	cases := map[string]func(f *installFixture) []string{
		"file": func(f *installFixture) []string {
			p := filepath.Join(f.home, "code")
			os.WriteFile(p, []byte(code+"\n"), 0o600)
			return []string{"-pair-code-file", p}
		},
		"env": func(f *installFixture) []string {
			f.in.getenv = func(k string) string {
				if k == "DUMB_PAIR_CODE" {
					return code
				}
				return ""
			}
			return []string{}
		},
		"stdin": func(f *installFixture) []string {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			w.WriteString(code + "\n")
			w.Close()
			t.Cleanup(func() { r.Close() })
			f.in.stdin = r
			return []string{"-pair-code-stdin"}
		},
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t)
			args := append([]string{"-server", "https://dumb.example", "-models", "m", "-dir", filepath.Join(f.home, "w")}, source(f)...)
			if err := f.install(args...); err != nil {
				t.Fatal(err)
			}
			if len(f.pairs) != 1 || f.pairs[0] != "https://dumb.example "+code {
				t.Fatalf("pairs %q", f.pairs)
			}
			token := filepath.Join(f.home, "w", "token")
			if read(t, token) != "paired-token-"+code+"\n" || mode(t, token) != 0o600 {
				t.Fatalf("token %q", read(t, token))
			}
			if strings.Contains(f.out.String(), code) || strings.Contains(strings.Join(f.runner.calls, " "), code) {
				t.Fatal("the pairing code leaked into output or commands")
			}
			// Re-running with the (now consumed) code keeps the stored credential.
			if name == "stdin" {
				return
			}
			if err := f.install(args...); err != nil {
				t.Fatal(err)
			}
			if len(f.pairs) != 1 || !strings.Contains(f.out.String(), "not used") {
				t.Fatalf("re-run paired again: %q, %q", f.pairs, f.out.String())
			}
			// -force-pair replaces it.
			if err := f.install(append(args, "-force-pair")...); err != nil {
				t.Fatal(err)
			}
			if len(f.pairs) != 2 {
				t.Fatalf("-force-pair did not pair: %q", f.pairs)
			}
		})
	}
}

func TestInstallSystem(t *testing.T) {
	f := newInstallFixture(t)
	f.in.euid = 0
	f.runner.fail = map[string]error{}
	// useradd "creates" the account.
	created := false
	f.in.runner = runnerFunc(func(line string) error {
		f.runner.calls = append(f.runner.calls, line)
		if strings.HasPrefix(line, "useradd ") {
			created = true
			f.users["dumb"] = [2]int{998, 997}
		}
		return nil
	})
	if err := f.install("-system", "-server", "https://dumb.example", "-models", "m", "-token-file", f.tokenFile("issued")); err != nil {
		t.Fatal(err)
	}
	if !created || !strings.HasPrefix(f.runner.calls[0], "useradd --system --user-group") {
		t.Fatalf("account not created first: %q", f.runner.calls)
	}
	bin := filepath.Join(f.root, "opt", "dumb-worker", "dumb-worker")
	token := filepath.Join(f.root, "etc", "dumb-worker", "token")
	staged := func(p string) string { return filepath.Join(filepath.Dir(p), "<staged>") }
	for _, p := range []string{filepath.Dir(bin), staged(bin), filepath.Dir(token), staged(token)} {
		if f.chowns[p] != [2]int{998, 997} {
			t.Errorf("%s owner %v, want dumb (998:997)", p, f.chowns[p])
		}
	}
	if mode(t, filepath.Dir(token)) != 0o700 || mode(t, token) != 0o600 {
		t.Errorf("credential dir %v, token %v", mode(t, filepath.Dir(token)), mode(t, token))
	}
	u := read(t, filepath.Join(f.root, "etc", "systemd", "system", "dumb-worker.service"))
	for _, want := range []string{"User=dumb", "ProtectSystem=strict", `ReadWritePaths="` + filepath.Dir(bin) + `"`, "WantedBy=multi-user.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("system unit lacks %s", want)
		}
	}
	joined := strings.Join(f.runner.calls, "|")
	if strings.Contains(joined, "systemctl --user") || !strings.Contains(joined, "systemctl enable --now dumb-worker.service") {
		t.Errorf("commands %q", f.runner.calls)
	}
	// Second run: the account exists; nothing is created again.
	f.runner.calls = nil
	created = false
	if err := f.install("-system", "-server", "https://dumb.example", "-models", "m"); err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("useradd ran for an existing account")
	}
}

type runnerFunc func(line string) error

func (r runnerFunc) Run(_ context.Context, name string, args ...string) error {
	return r(strings.TrimSpace(name + " " + strings.Join(args, " ")))
}

func TestInstallRefusesBadInput(t *testing.T) {
	cases := map[string]struct {
		adjust func(f *installFixture)
		args   []string
	}{
		"not linux":          {func(f *installFixture) { f.in.goos = "darwin" }, nil},
		"both modes":         {nil, []string{"-system", "-user"}},
		"system as non-root": {nil, []string{"-system"}},
		"user as root":       {func(f *installFixture) { f.in.euid = 0 }, nil},
		"system with dir":    {func(f *installFixture) { f.in.euid = 0 }, []string{"-system", "-dir", "/x"}},
		"no models":          {nil, []string{"-models", ""}},
		"plain http":         {nil, []string{"-server", "http://dumb.example"}},
		"public ollama":      {nil, []string{"-ollama", "http://8.8.8.8:11434"}},
		"bad service name":   {nil, []string{"-service-name", "../x"}},
		"two sources":        {nil, []string{"-pair-code-file", "/x", "-pair-code-stdin"}},
		"force without code": {nil, []string{"-force-pair"}},
		"no credential":      {nil, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t)
			if c.adjust != nil {
				c.adjust(f)
			}
			args := append([]string{"-server", "https://dumb.example", "-models", "m"}, c.args...)
			err := f.install(args...)
			var usage usageError
			if !errors.As(err, &usage) {
				t.Fatalf("got %v, want a usage error", err)
			}
			if len(f.runner.calls) != 0 || f.setups != 0 {
				t.Fatalf("touched the system: %q, setups %d", f.runner.calls, f.setups)
			}
		})
	}
}

func TestInstallSetupFailureInstallsNoService(t *testing.T) {
	f := newInstallFixture(t)
	f.setupErr = errors.New("connection refused")
	err := f.install("-server", "https://dumb.example", "-models", "m", "-token-file", f.tokenFile("t"))
	if err == nil || !strings.Contains(err.Error(), "-skip-setup") || len(f.runner.calls) != 0 {
		t.Fatalf("err %v, commands %q", err, f.runner.calls)
	}
	if err := f.install("-server", "https://dumb.example", "-models", "m", "-skip-setup"); err != nil {
		t.Fatal(err)
	}
	if f.setups != 1 || len(f.runner.calls) == 0 {
		t.Fatalf("-skip-setup: setups %d, commands %q", f.setups, f.runner.calls)
	}
}

func TestInstallRespectsProbationAndSymlinks(t *testing.T) {
	f := newInstallFixture(t)
	args := []string{"-server", "https://dumb.example", "-models", "m", "-token-file", f.tokenFile("t")}
	if err := f.install(args...); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(f.home, ".local", "share", "dumb-worker", "bin", "dumb-worker")
	os.WriteFile(bin+".update.json", []byte("{}"), 0o600)
	os.WriteFile(f.in.exe, []byte("binary v2"), 0o755)
	if err := f.install(args...); err == nil || !strings.Contains(err.Error(), "probation") {
		t.Fatalf("got %v, want a probation refusal", err)
	}
	if read(t, bin) != "binary v1" {
		t.Fatal("binary replaced during probation")
	}
	os.Remove(bin + ".update.json")
	token := filepath.Join(f.home, ".local", "share", "dumb-worker", "token")
	os.Remove(token)
	os.Symlink(filepath.Join(f.home, "elsewhere"), token)
	if err := f.install(args...); err == nil {
		t.Fatal("a symlinked credential path was accepted")
	}
	if _, err := os.Stat(filepath.Join(f.home, "elsewhere")); err == nil {
		t.Fatal("wrote through the symlink")
	}
}

func TestInstallCustomServiceNameAndRemote(t *testing.T) {
	f := newInstallFixture(t)
	if err := f.install("-server", "http://127.0.0.1:18080", "-models", "m", "-token-file", f.tokenFile("t"),
		"-service-name", "dumb-worker-accept", "-channel", "beta", "-ollama-remote", "-ollama", "http://192.168.10.20:11434"); err != nil {
		t.Fatal(err)
	}
	u := read(t, filepath.Join(f.home, ".config", "systemd", "user", "dumb-worker-accept.service"))
	if !strings.Contains(u, `"DUMB_CHANNEL=beta"`) || !strings.Contains(u, `"DUMB_SERVER=http://127.0.0.1:18080"`) ||
		!strings.Contains(u, `"DUMB_OLLAMA_REMOTE=1"`) || !strings.Contains(u, `"DUMB_OLLAMA=http://192.168.10.20:11434"`) {
		t.Fatalf("unit:\n%s", u)
	}
}

func TestInstallDispatch(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"install", "-system", "-user", "-server", "https://x.example", "-models", "m"}, &out, &errOut); code != exitUsage {
		t.Fatalf("exit %d, want %d: %s", code, exitUsage, errOut.String())
	}
	if code := run(context.Background(), []string{"install", "-h"}, &out, &errOut); code != exitOK || !strings.Contains(errOut.String(), "-pair-code-file") {
		t.Fatalf("help: exit %d: %s", code, errOut.String())
	}
}
