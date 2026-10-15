package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/service"
	"github.com/gkhntpbs/dumb/internal/update"
	"github.com/gkhntpbs/dumb/internal/worker"
)

// installUsage documents the non-interactive Linux install.
const installUsage = `usage: dumb-worker install -server URL -models LIST [-user | -system] [credential] [flags]

Installs this binary, a restricted credential and a systemd unit, brings Ollama
to readiness and starts the service. Linux only. Safe to run again: unchanged
parts are left alone and the service restarts only when something changed.

Credential (one of; the stored credential is reused when none is given):
  -pair-code-file PATH   read a one-time pairing code from PATH
  -pair-code-stdin       read the pairing code from stdin (hidden on a terminal)
  DUMB_PAIR_CODE         environment variable holding the pairing code
  -token-file PATH       copy a pre-issued worker token from PATH
  -force-pair            replace an existing credential with a new pairing`

// installer holds every seam to the machine so tests never touch it.
type installer struct {
	goos       string
	euid       int
	home       string
	username   string
	root       string // prefix for absolute system paths; "/" in production
	exe        string // the binary being installed (symlinks resolved)
	stdin      *os.File
	stdout     io.Writer
	log        *log.Logger
	getenv     func(string) string
	runner     service.Runner
	lookupUser func(name string) (uid, gid int, err error)
	chown      func(path string, uid, gid int) error
	pair       func(ctx context.Context, server, code string) (string, error)
	setup      func(ctx context.Context, c *config) error
	linger     func(username string) bool
}

type installOptions struct {
	cfg                      *config
	system, user             bool
	dir                      string
	serviceName              string
	pairCodeFile, tokenFile  string
	pairCodeStdin, forcePair bool
	skipSetup                bool
}

// installLayout is where the files go.
type installLayout struct {
	binDir, bin    string
	confDir, token string
	uid, gid       int // owner of binDir, confDir and token
	chown          bool
}

func parseInstallFlags(args []string, stderr io.Writer) (*installOptions, error) {
	o := &installOptions{}
	c := &config{ollamaConfigured: os.Getenv("DUMB_OLLAMA") != "", headless: true}
	fs := flag.NewFlagSet("dumb-worker install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, installUsage); fs.PrintDefaults() }
	capDefault, err := strconv.Atoi(env("DUMB_CAPACITY", "1"))
	if err != nil {
		return nil, fmt.Errorf("DUMB_CAPACITY: %w", err)
	}
	fs.StringVar(&c.server, "server", os.Getenv("DUMB_SERVER"), "server URL (env DUMB_SERVER)")
	fs.StringVar(&c.models, "models", os.Getenv("DUMB_MODELS"), "comma-separated models (env DUMB_MODELS)")
	fs.IntVar(&c.capacity, "capacity", capDefault, "concurrent jobs (env DUMB_CAPACITY)")
	fs.StringVar(&c.name, "name", os.Getenv("DUMB_WORKER_NAME"), nameUsage)
	fs.StringVar(&c.ollamaURL, "ollama", env("DUMB_OLLAMA", ollama.DefaultURL), "Ollama URL (env DUMB_OLLAMA)")
	fs.BoolVar(&c.ollamaRemote, "ollama-remote", os.Getenv("DUMB_OLLAMA_REMOTE") == "1", "use an Ollama on a private-network host; never managed (env DUMB_OLLAMA_REMOTE=1)")
	fs.StringVar(&c.channel, "channel", env("DUMB_CHANNEL", update.ChannelStable), "update channel: stable or beta (env DUMB_CHANNEL)")
	fs.StringVar(&c.updateURL, "update-url", env("DUMB_UPDATE_URL", update.DefaultBaseURL), "GitHub API base URL for releases (env DUMB_UPDATE_URL)")
	fs.BoolVar(&o.system, "system", false, "install a hardened system unit running as user \"dumb\" (needs root)")
	fs.BoolVar(&o.user, "user", false, "install a systemd user unit for the current account (default)")
	fs.StringVar(&o.dir, "dir", "", "user install: directory for the binary and credential (default ~/.local/share/dumb-worker)")
	fs.StringVar(&o.serviceName, "service-name", service.DefaultLabel, "systemd unit name")
	fs.StringVar(&o.pairCodeFile, "pair-code-file", "", "file holding a one-time pairing code")
	fs.BoolVar(&o.pairCodeStdin, "pair-code-stdin", false, "read the pairing code from stdin")
	fs.BoolVar(&o.forcePair, "force-pair", false, "pair again even when a credential is stored")
	fs.StringVar(&o.tokenFile, "token-file", "", "pre-issued worker token to copy into place")
	fs.BoolVar(&o.skipSetup, "skip-setup", false, "do not check Ollama and the models before starting the service")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "ollama" {
			c.ollamaConfigured = true
		}
	})
	if err := c.checkName(); err != nil {
		return nil, err
	}
	o.cfg = c
	return o, nil
}

// checkOllamaURL validates the configured Ollama endpoint without contacting it.
func checkOllamaURL(c *config) error {
	_, err := c.ollamaClient()
	return err
}

func cmdInstall(ctx context.Context, args []string, stdout, stderr io.Writer, logger *log.Logger) int {
	o, err := parseInstallFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintln(stderr, "dumb-worker install:", err)
		return exitUsage
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		logger.Print(err)
		return exitError
	}
	home, _ := os.UserHomeDir()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	in := &installer{
		goos: runtime.GOOS, euid: os.Geteuid(), home: home, username: username, root: "/", exe: exe,
		stdin: os.Stdin, stdout: stdout, log: logger, getenv: os.Getenv, runner: loggingRunner{logger},
		lookupUser: lookupUser, chown: os.Chown,
		pair: func(ctx context.Context, server, code string) (string, error) {
			return exchangePair(ctx, http.DefaultClient, server, code)
		},
		setup: func(ctx context.Context, c *config) error { return runSetup(ctx, c, logger) },
		linger: func(name string) bool {
			_, err := os.Stat(filepath.Join("/var/lib/systemd/linger", name))
			return err == nil
		},
	}
	if err := in.install(ctx, o); err != nil {
		logger.Print("install: ", err)
		var usage usageError
		if errors.As(err, &usage) {
			return exitUsage
		}
		return exitError
	}
	return exitOK
}

// usageError marks a configuration mistake (exit code 2).
type usageError struct{ error }

func usagef(format string, args ...any) error { return usageError{fmt.Errorf(format, args...)} }

func lookupUser(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// loggingRunner runs system commands and says which.
type loggingRunner struct{ log *log.Logger }

func (r loggingRunner) Run(ctx context.Context, name string, args ...string) error {
	r.log.Printf("install: running %s %s", name, strings.Join(args, " "))
	return ollama.ExecRunner{}.Run(ctx, name, args...)
}

func (in *installer) path(p string) string { return filepath.Join(in.root, p) }

func (in *installer) layout(o *installOptions) (installLayout, error) {
	if o.system {
		uid, gid, err := in.lookupUser(service.DefaultUser)
		if err != nil {
			return installLayout{}, err
		}
		binDir, confDir := in.path("/opt/dumb-worker"), in.path("/etc/dumb-worker")
		return installLayout{binDir: binDir, bin: filepath.Join(binDir, update.BinaryName("linux")),
			confDir: confDir, token: filepath.Join(confDir, "token"), uid: uid, gid: gid, chown: true}, nil
	}
	dir := o.dir
	if dir == "" {
		if in.home == "" {
			return installLayout{}, usagef("cannot find the home directory; pass -dir")
		}
		dir = filepath.Join(in.home, ".local", "share", "dumb-worker")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return installLayout{}, err
	}
	binDir := filepath.Join(dir, "bin")
	return installLayout{binDir: binDir, bin: filepath.Join(binDir, update.BinaryName("linux")),
		confDir: dir, token: filepath.Join(dir, "token"), uid: in.euid, gid: -1}, nil
}

func (in *installer) validate(o *installOptions) error {
	c := o.cfg
	if in.goos != "linux" {
		return usagef("install is for Linux; on macOS and Windows use the desktop app or `dumb-worker service install`")
	}
	if o.system && o.user {
		return usagef("choose one of -system and -user")
	}
	if o.system && o.dir != "" {
		return usagef("-dir applies to user installs only")
	}
	if o.system && in.euid != 0 {
		return usagef("-system needs root (run it with sudo)")
	}
	if !o.system && in.euid == 0 {
		return usagef("refusing a user install as root; run as the account that owns the worker, or use -system")
	}
	if _, err := worker.CheckServerURL(c.server); err != nil {
		return usageError{err}
	}
	if len(c.modelList()) == 0 {
		return usagef("-models is required")
	}
	if c.capacity < 1 {
		return usagef("-capacity must be at least 1")
	}
	if c.channel != update.ChannelStable && c.channel != update.ChannelBeta {
		return usagef("channel %q: want stable or beta", c.channel)
	}
	if err := checkOllamaURL(c); err != nil {
		return usageError{err}
	}
	if o.serviceName == "" || strings.ContainsAny(o.serviceName, "/ \t\n\\") || strings.HasSuffix(o.serviceName, ".service") {
		return usagef("invalid -service-name %q", o.serviceName)
	}
	sources := 0
	for _, set := range []bool{o.pairCodeFile != "", o.pairCodeStdin, in.getenv("DUMB_PAIR_CODE") != "", o.tokenFile != ""} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return usagef("give at most one of -pair-code-file, -pair-code-stdin, DUMB_PAIR_CODE and -token-file")
	}
	if o.forcePair && sources == 0 {
		return usagef("-force-pair needs a pairing code")
	}
	return nil
}

func (in *installer) install(ctx context.Context, o *installOptions) error {
	if err := in.validate(o); err != nil {
		return err
	}
	c := o.cfg
	if o.system {
		if err := in.ensureServiceUser(ctx); err != nil {
			return err
		}
	}
	l, err := in.layout(o)
	if err != nil {
		return err
	}
	if err := in.ensureDir(l.binDir, 0o755, l); err != nil {
		return err
	}
	if err := in.ensureDir(l.confDir, 0o700, l); err != nil {
		return err
	}
	changed := false
	binChanged, err := in.installBinary(l)
	if err != nil {
		return err
	}
	tokenChanged, err := in.installCredential(ctx, o, l)
	if err != nil {
		return err
	}
	changed = binChanged || tokenChanged
	if !o.skipSetup {
		fmt.Fprintln(in.stdout, "Checking Ollama and the models…")
		if err := in.setup(ctx, c); err != nil {
			return fmt.Errorf("Ollama is not ready (%w); fix it and run install again, or pass -skip-setup to start the service anyway", err)
		}
	}
	sc := service.Config{GOOS: "linux", Label: o.serviceName, Binary: l.bin, Env: installEnv(c, l.token),
		Home: in.home, System: o.system, Root: in.root, Runner: in.runner}
	unitPath := service.Path(sc)
	old, readErr := os.ReadFile(unitPath)
	existed := readErr == nil
	if !existed || string(old) != service.SystemdUnit(sc) {
		changed = true
	}
	if err := service.Install(ctx, sc); err != nil {
		return fmt.Errorf("service: %w", err)
	}
	systemctl := []string{"--user"}
	if o.system {
		systemctl = nil
	}
	unit := o.serviceName + ".service"
	if existed && changed {
		if err := in.runner.Run(ctx, "systemctl", append(systemctl, "restart", unit)...); err != nil {
			return fmt.Errorf("service restart: %w", err)
		}
	}
	if err := in.runner.Run(ctx, "systemctl", append(systemctl, "is-active", "--quiet", unit)...); err != nil {
		return fmt.Errorf("service %s is not active after start: %w", unit, err)
	}
	state := "unchanged"
	if changed {
		state = "updated"
	}
	if !existed {
		state = "installed"
	}
	journal := "journalctl --user -u " + unit + " -f"
	if o.system {
		journal = "journalctl -u " + unit + " -f"
	}
	fmt.Fprintf(in.stdout, "dumb-worker %s: %s is running.\n  binary     %s\n  credential %s\n  unit       %s\n  logs       %s\n",
		state, unit, l.bin, l.token, unitPath, journal)
	if !o.system && !in.linger(in.username) {
		fmt.Fprintf(in.stdout, "Note: a user unit stops when you log out and does not start at boot unless lingering is on:\n  sudo loginctl enable-linger %s\n", in.username)
	}
	return nil
}

// installEnv is the service configuration (no secrets: the token stays in its file).
func installEnv(c *config, token string) map[string]string {
	envs := map[string]string{"DUMB_SERVER": c.server, "DUMB_MODELS": strings.Join(c.modelList(), ","),
		"DUMB_CAPACITY": strconv.Itoa(c.capacity), "DUMB_TOKEN_FILE": token, "DUMB_HEADLESS": "1"}
	c.addOllamaEnvironment(envs)
	c.addNameEnvironment(envs)
	if c.channel != update.ChannelStable {
		envs["DUMB_CHANNEL"] = c.channel
	}
	if c.updateURL != update.DefaultBaseURL {
		envs["DUMB_UPDATE_URL"] = c.updateURL
	}
	return envs
}

func (in *installer) ensureServiceUser(ctx context.Context) error {
	_, _, err := in.lookupUser(service.DefaultUser)
	if err == nil {
		return nil
	}
	var unknown user.UnknownUserError
	if !errors.As(err, &unknown) {
		return err
	}
	return in.runner.Run(ctx, "useradd", "--system", "--user-group", "--home-dir", "/var/lib/dumb-worker",
		"--create-home", "--shell", "/usr/sbin/nologin", service.DefaultUser)
}

// ensureDir creates dir (and parents) or checks an existing one: it must be a
// real directory, not a symlink. The final mode and owner are enforced.
func (in *installer) ensureDir(dir string, mode os.FileMode, l installLayout) error {
	fi, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(dir, mode); err != nil {
			return err
		}
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
		return fmt.Errorf("%s must be a directory, not a symlink or file", dir)
	}
	if err := os.Chmod(dir, mode); err != nil {
		return err
	}
	if l.chown {
		return in.chown(dir, l.uid, l.gid)
	}
	return nil
}

// installBinary puts this binary at l.bin unless it is already there.
func (in *installer) installBinary(l installLayout) (bool, error) {
	self, err := os.ReadFile(in.exe)
	if err != nil {
		return false, err
	}
	if fi, err := os.Lstat(l.bin); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return false, fmt.Errorf("%s must be a regular file", l.bin)
		}
		if cur, err := os.ReadFile(l.bin); err == nil && sha256.Sum256(cur) == sha256.Sum256(self) {
			return false, nil
		}
		if _, err := os.Stat(l.bin + ".update.json"); err == nil {
			return false, fmt.Errorf("a self-update of %s is on probation; run install again once it has committed or rolled back", l.bin)
		}
	}
	return true, in.writeAtomic(l.bin, self, 0o755, l)
}

// installCredential stores the worker credential; it reports whether it changed.
func (in *installer) installCredential(ctx context.Context, o *installOptions, l installLayout) (bool, error) {
	_, statErr := os.Lstat(l.token)
	exists := statErr == nil
	if exists {
		if _, err := worker.ReadTokenFile(l.token); err != nil {
			return false, fmt.Errorf("stored credential: %w", err)
		}
	}
	switch {
	case o.tokenFile != "":
		token, err := worker.ReadTokenFile(o.tokenFile)
		if err != nil {
			return false, err
		}
		if exists {
			if cur, err := worker.ReadTokenFile(l.token); err == nil && cur == token {
				return false, in.fixOwner(l.token, l)
			}
		}
		return true, in.writeAtomic(l.token, []byte(token+"\n"), 0o600, l)
	case o.pairCodeFile != "" || o.pairCodeStdin || in.getenv("DUMB_PAIR_CODE") != "":
		if exists && !o.forcePair {
			fmt.Fprintln(in.stdout, "A credential is already stored; the pairing code was not used (pass -force-pair to replace it).")
			return false, in.fixOwner(l.token, l)
		}
		code, err := in.pairCode(o)
		if err != nil {
			return false, err
		}
		token, err := in.pair(ctx, o.cfg.server, code)
		if err != nil {
			return false, err
		}
		if err := in.writeAtomic(l.token, []byte(token+"\n"), 0o600, l); err != nil {
			return false, fmt.Errorf("credential was issued but could not be stored; obtain a new pairing code: %w", err)
		}
		fmt.Fprintln(in.stdout, "Paired: worker credential saved.")
		return true, nil
	case exists:
		return false, in.fixOwner(l.token, l)
	}
	return false, usagef("no credential stored at %s: give a pairing code (-pair-code-file, -pair-code-stdin or DUMB_PAIR_CODE) or -token-file", l.token)
}

func (in *installer) fixOwner(path string, l installLayout) error {
	if !l.chown {
		return nil
	}
	return in.chown(path, l.uid, l.gid)
}

// pairCode reads the one-time code; it never appears in arguments or logs.
func (in *installer) pairCode(o *installOptions) (string, error) {
	var raw string
	switch {
	case o.pairCodeFile != "":
		f, err := os.Open(o.pairCodeFile)
		if err != nil {
			return "", err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 129))
		if err != nil {
			return "", err
		}
		raw = string(b)
	case o.pairCodeStdin:
		if in.stdin == nil {
			return "", errors.New("no stdin")
		}
		fmt.Fprint(in.stdout, "Pairing code (hidden): ")
		restore, err := pairHideInput(in.stdin)
		if err != nil {
			return "", errors.New("cannot disable input echo")
		}
		line, err := bufio.NewReader(io.LimitReader(in.stdin, 129)).ReadString('\n')
		restore()
		fmt.Fprintln(in.stdout)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", errors.New("cannot read the pairing code")
		}
		raw = line
	default:
		raw = in.getenv("DUMB_PAIR_CODE")
	}
	code := strings.TrimSpace(raw)
	if code == "" || len(code) > 128 {
		return "", errors.New("the pairing code is empty or too long")
	}
	return code, nil
}

// writeAtomic writes data next to path, syncs, sets mode and owner, renames it
// into place and syncs the directory. A symlink at path is refused.
func (in *installer) writeAtomic(path string, data []byte, mode os.FileMode, l installLayout) error {
	if fi, err := os.Lstat(path); err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular()) {
		return fmt.Errorf("%s must be a regular file", path)
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".dumb-install-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if l.chown {
		if err := in.chown(tmp, l.uid, l.gid); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
