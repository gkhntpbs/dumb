// dumb-worker pulls jobs from dumb-server and runs them on the local Ollama.
// It accepts no inbound connections.
//
// Usage:
//
//	dumb-worker [run]                         serve jobs (default)
//	dumb-worker doctor                        check this machine and the configuration
//	dumb-worker setup                         install/start Ollama and pull the models
//	dumb-worker install -server URL -models M  Linux: one-command non-interactive install (see docs/linux-install.md)
//	dumb-worker service install|uninstall|print
//	dumb-worker version
//
// Configuration (flag, then environment):
//
//	-server      DUMB_SERVER      server URL (https; http only on loopback)
//	-token-file  DUMB_TOKEN_FILE  file holding this worker's token (mode 0600)
//	-models      DUMB_MODELS      comma-separated Ollama models to serve
//	-capacity    DUMB_CAPACITY    jobs run at the same time (default 1)
//	-ollama      DUMB_OLLAMA      Ollama URL (default http://127.0.0.1:11434; loopback only)
//	-ollama-remote DUMB_OLLAMA_REMOTE=1  allow an Ollama on a private-network host (never managed)
//	-channel     DUMB_CHANNEL     update channel: stable (default) or beta
//	-update-url  DUMB_UPDATE_URL  GitHub API base URL for releases (default https://api.github.com)
//
// Exit codes: 0 stopped, 1 error, 2 usage, 4 restart (an update or a rollback
// was applied; the service manager starts the binary now in place).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/gkhntpbs/dumb/internal/branding"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gkhntpbs/dumb/internal/doctor"
	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/service"
	"github.com/gkhntpbs/dumb/internal/update"
	"github.com/gkhntpbs/dumb/internal/worker"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0"

// Exit codes.
const (
	exitOK      = 0
	exitError   = 1
	exitUsage   = 2
	exitRestart = 4 // an update or rollback was applied: start the binary again now
)

// Update timing.
const (
	updateEvery    = 6 * time.Hour
	updateJitter   = time.Hour
	upgradeWait    = 15 * time.Minute // after a 426 with no usable update; doubles
	upgradeMaxWait = 2 * time.Hour
)

func main() {
	if _, err := protocol.ParseReleaseVersion(version); err != nil {
		fmt.Fprintln(os.Stderr, "dumb-worker: invalid build version:", err)
		os.Exit(exitError)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := platformRun(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type config struct {
	server, tokenFile, models, ollamaURL string
	channel, updateURL                   string
	logDir                               string // service: macOS log directory
	serviceName                          string // service: launchd label / systemd unit / Windows service name
	capacity                             int
	system                               bool // service: Linux system unit
	headless                             bool
	ollamaConfigured                     bool
	ollamaRemote                         bool // -ollama-remote: private-network Ollama, not managed
	ollamaDataDir                        string
	ollamaModelsDir                      string
	newSystem                            func(*ollama.Client) *ollama.System
	terminalColumns                      func(io.Writer) int
}

func (c *config) ollamaSystem(client *ollama.Client) *ollama.System {
	system := ollama.NewSystem(client)
	if c.newSystem != nil {
		system = c.newSystem(client)
	}
	if system.WindowsDataDir == "" {
		system.WindowsDataDir = c.ollamaDataDir
	}
	if os.Getenv("OLLAMA_MODELS") == "" {
		system.WindowsModelsDir = c.ollamaModelsDir
	}
	return system
}

func (c *config) modelList() []string {
	var out []string
	for _, m := range strings.Split(c.models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseFlags(name string, args []string, stderr io.Writer) (*config, []string, error) {
	c := &config{ollamaConfigured: os.Getenv("DUMB_OLLAMA") != ""}
	fs := flag.NewFlagSet("dumb-worker "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	capDefault, err := strconv.Atoi(env("DUMB_CAPACITY", "1"))
	if err != nil {
		return nil, nil, fmt.Errorf("DUMB_CAPACITY: %w", err)
	}
	fs.BoolVar(&c.headless, "headless", os.Getenv("DUMB_HEADLESS") == "1", "run without the desktop status UI (env DUMB_HEADLESS=1)")
	fs.StringVar(&c.server, "server", os.Getenv("DUMB_SERVER"), "server URL (env DUMB_SERVER)")
	fs.StringVar(&c.tokenFile, "token-file", os.Getenv("DUMB_TOKEN_FILE"), "token file, mode 0600 (env DUMB_TOKEN_FILE)")
	fs.StringVar(&c.models, "models", os.Getenv("DUMB_MODELS"), "comma-separated models (env DUMB_MODELS)")
	fs.IntVar(&c.capacity, "capacity", capDefault, "concurrent jobs (env DUMB_CAPACITY)")
	fs.StringVar(&c.ollamaURL, "ollama", env("DUMB_OLLAMA", ollama.DefaultURL), "Ollama URL, loopback only unless -ollama-remote (env DUMB_OLLAMA)")
	fs.BoolVar(&c.ollamaRemote, "ollama-remote", os.Getenv("DUMB_OLLAMA_REMOTE") == "1", "use an Ollama on a private-network host (10/8, 172.16/12, 192.168/16, fc00::/7); it is never installed, started or pulled to (env DUMB_OLLAMA_REMOTE=1)")
	fs.StringVar(&c.channel, "channel", env("DUMB_CHANNEL", update.ChannelStable), "update channel: stable or beta (env DUMB_CHANNEL)")
	fs.StringVar(&c.updateURL, "update-url", env("DUMB_UPDATE_URL", update.DefaultBaseURL), "GitHub API base URL for releases (env DUMB_UPDATE_URL)")
	if name == "service" {
		fs.BoolVar(&c.system, "system", false, "Linux: install a system unit instead of a user unit")
		fs.StringVar(&c.logDir, "log-dir", "", "macOS: directory for dumb-worker.log (default ~/Library/Logs)")
		fs.StringVar(&c.serviceName, "service-name", service.DefaultLabel, "service name (launchd label, systemd unit, Windows service)")
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == "ollama" {
			c.ollamaConfigured = true
		}
	})
	if c.channel != update.ChannelStable && c.channel != update.ChannelBeta {
		return nil, nil, fmt.Errorf("channel %q: want stable or beta", c.channel)
	}
	return c, fs.Args(), nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	logger := log.New(stderr, "dumb-worker: ", log.LstdFlags)
	var sub string
	if cmd == "service" {
		if len(args) == 0 {
			fmt.Fprintln(stderr, "usage: dumb-worker service install|uninstall|print [flags]")
			return exitUsage
		}
		sub, args = args[0], args[1:]
	}
	if cmd == "version" {
		fmt.Fprint(stdout, branding.LogoCompact)
		fmt.Fprintf(stdout, "dumb-worker %s (protocol v%d)\n", version, protocol.Version)
		return exitOK
	}
	if cmd == "install" {
		return cmdInstall(ctx, args, stdout, stderr, logger)
	}
	if cmd == "pair" {
		c, err := parsePairFlags(args)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		return cmdPair(ctx, c, stdout, stderr)
	}
	c, rest, err := parseFlags(cmd, args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintln(stderr, "dumb-worker:", err)
		return exitUsage
	}
	if len(rest) > 0 {
		fmt.Fprintf(stderr, "dumb-worker: unexpected arguments %q\n", rest)
		return exitUsage
	}
	switch cmd {
	case "run":
		return cmdRun(ctx, c, logger)
	case "doctor":
		return cmdDoctor(ctx, c, stdout)
	case "setup":
		return cmdSetup(ctx, c, stdout, logger)
	case "service":
		return cmdService(ctx, c, sub, stdout, logger)
	}
	fmt.Fprintf(stderr, "dumb-worker: unknown command %q (run, doctor, setup, install, pair, service, version)\n", cmd)
	return exitUsage
}

func cmdRun(ctx context.Context, c *config, logger *log.Logger) int {
	token, err := worker.ReadTokenFile(c.tokenFile)
	if err != nil {
		logger.Print(err)
		return exitError
	}
	client, err := c.ollamaClient()
	if err != nil {
		logger.Print(err)
		return exitError
	}
	system := c.ollamaSystem(client)
	recovery, err := ollama.NewRecovery(ollama.RecoveryConfig{System: system, Models: c.modelList(), External: c.ollamaConfigured, Logger: logger})
	if err != nil {
		logger.Print(err)
		return exitError
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := recovery.Close(stop); err != nil {
			logger.Print(err)
		}
	}()
	wcfg := worker.Config{
		ServerURL: c.server, Token: token, Models: c.modelList(), Capacity: c.capacity,
		OllamaURL: c.ollamaURL, Version: version, Logger: logger,
		Ollama: system.Client, Recovery: recovery, ModelCheck: 5 * time.Second,
	}
	if _, err := worker.New(wcfg); err != nil { // validate before touching updates
		logger.Print(err)
		return exitError
	}
	logger.Printf("version %s, server %s, channel %s", version, c.server, c.channel)
	s := &supervisor{
		version: version, log: logger,
		every: updateEvery, jitter: updateJitter, backoff: upgradeWait, maxBack: upgradeMaxWait,
	}
	s.newWorker = func(onHello func()) (runner, error) {
		cfg := wcfg
		cfg.OnHello = onHello
		cfg.OnRecommended = s.recommended
		cfg.OnStatus = desktopStatusObserver(wcfg.OnStatus, func() { platformReady(desktopRuntimeInfo{OllamaPID: system.OwnedPID(), OllamaURL: c.ollamaURL}) })
		return worker.New(cfg)
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	switch {
	case err != nil:
		logger.Printf("update: cannot locate this binary (%v); self-update disabled", err)
	case update.PublicKey == "":
		logger.Print(update.ErrDisabled)
	default:
		s.upd = &update.Updater{
			Exe: exe, Version: version, Channel: c.channel, Key: update.PublicKey,
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Logger: logger,
			Source: &update.Source{BaseURL: c.updateURL, UserAgent: "dumb-worker/" + version,
				HTTP: &http.Client{Timeout: 10 * time.Minute}},
		}
	}
	if err == nil {
		// Probation applies even with updates disabled: a rollback needs no key.
		p, perr := update.Startup(exe, version, update.DefaultWindow, update.DefaultMaxAttempts, logger)
		if errors.Is(perr, update.ErrRestart) {
			return exitRestart
		}
		if perr != nil {
			logger.Print(perr)
		}
		s.probation = p
	}
	if (runtime.GOOS == "darwin" || runtime.GOOS == "windows") && !c.headless {
		session, uerr := startMenuSession(ctx, exe)
		if uerr != nil {
			logger.Printf("desktop status UI unavailable; no jobs will be claimed: %v", uerr)
			if s.probation != nil && errors.Is(s.probation.Rollback(), update.ErrRestart) {
				return exitRestart
			}
			<-ctx.Done()
			return exitOK
		}
		defer session.Close()
		view := &menuView{session: session, mode: "starting", category: "none"}
		s.controls, s.uiFailed, s.status = session.Events(), session.Failed(), view.lifecycle
		wcfg.OnStatus = view.worker
		view.lifecycle("starting", "none")
	}
	return s.run(ctx)
}

func cmdDoctor(ctx context.Context, c *config, stdout io.Writer) int {
	var pre []doctor.Result
	token, err := worker.ReadTokenFile(c.tokenFile)
	if err != nil {
		pre = append(pre, doctor.Result{Name: "Token file", Detail: err.Error()})
	} else {
		pre = append(pre, doctor.Result{Name: "Token file", OK: true, Detail: c.tokenFile})
	}
	if _, err := worker.CheckServerURL(c.server); err != nil {
		pre = append(pre, doctor.Result{Name: "Server URL", Detail: err.Error()})
	}
	oc, err := c.ollamaClient()
	if err != nil {
		pre = append(pre, doctor.Result{Name: "Ollama URL", Detail: err.Error()})
		doctor.Print(stdout, pre)
		return exitError
	}
	d := &doctor.Doctor{
		ServerURL: c.server, Token: token, Version: version, Models: c.modelList(), OllamaPort: oc.Port(), Remote: oc.Remote(),
		Ollama: oc, Detect: ollama.NewSystem(oc).Detect,
		HTTP:   &http.Client{Timeout: 10 * time.Second},
		Getenv: os.Getenv, InterfaceAddrs: net.InterfaceAddrs,
		Dial: (&net.Dialer{}).DialContext,
	}
	if !doctor.Print(stdout, append(pre, d.Run(ctx)...)) {
		return exitError
	}
	return exitOK
}

func cmdSetup(ctx context.Context, c *config, stdout io.Writer, logger *log.Logger) int {
	if _, err := c.ollamaClient(); err != nil {
		logger.Print(err)
		return exitError
	}
	columns := setupTerminalColumns
	if c.terminalColumns != nil {
		columns = c.terminalColumns
	}
	setupBanner(stdout, columns(stdout))
	if err := runSetup(ctx, c, logger); err != nil {
		logger.Print(err)
		return exitError
	}
	fmt.Fprintln(stdout, "Setup complete. Next: dumb-worker run")
	return exitOK
}

// runSetup brings Ollama to readiness for the configured models (bounded).
func runSetup(ctx context.Context, c *config, logger *log.Logger) error {
	oc, err := c.ollamaClient()
	if err != nil {
		return err
	}
	system := ollama.NewSystem(oc)
	if c.newSystem != nil {
		system = c.newSystem(oc)
	}
	recovery, err := ollama.NewRecovery(ollama.RecoveryConfig{System: system, Models: c.modelList(), External: c.ollamaConfigured, Logger: logger})
	if err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := recovery.Close(stop); err != nil {
			logger.Print(err)
		}
	}()
	return recovery.Ensure(ctx)
}

func cmdService(ctx context.Context, c *config, sub string, stdout io.Writer, logger *log.Logger) int {
	bin, err := os.Executable()
	if err == nil {
		bin, err = filepath.EvalSymlinks(bin)
	}
	if err != nil {
		logger.Print(err)
		return exitError
	}
	envs := map[string]string{"DUMB_SERVER": c.server, "DUMB_MODELS": c.models, "DUMB_CAPACITY": strconv.Itoa(c.capacity)}
	if c.tokenFile != "" {
		abs, err := filepath.Abs(c.tokenFile)
		if err != nil {
			logger.Print(err)
			return exitError
		}
		envs["DUMB_TOKEN_FILE"] = abs
	}
	if c.logDir != "" {
		if c.logDir, err = filepath.Abs(c.logDir); err != nil {
			logger.Print(err)
			return exitError
		}
	}
	c.addOllamaEnvironment(envs)
	if c.headless {
		envs["DUMB_HEADLESS"] = "1"
	}
	if c.channel != update.ChannelStable {
		envs["DUMB_CHANNEL"] = c.channel
	}
	if c.updateURL != update.DefaultBaseURL {
		envs["DUMB_UPDATE_URL"] = c.updateURL
	}
	home, _ := os.UserHomeDir()
	sc := service.Config{
		GOOS: runtime.GOOS, Label: c.serviceName, Binary: bin, Env: envs, Home: home, UID: os.Getuid(),
		System: c.system, LogDir: c.logDir, Runner: ollama.ExecRunner{},
	}
	switch sub {
	case "print":
		s, err := service.Print(sc)
		if err != nil {
			logger.Print(err)
			return exitError
		}
		if p := service.Path(sc); p != "" {
			fmt.Fprintf(stdout, "# %s\n", p)
		}
		if sc.System && sc.GOOS == "linux" {
			fmt.Fprintf(stdout, "# Runs as user %q. Create it first:\n# %s\n", service.DefaultUser,
				strings.ReplaceAll(service.SystemUserHelp, "\n", "\n# "))
		}
		fmt.Fprint(stdout, s)
		return exitOK
	case "install", "uninstall":
		if sub == "install" && (c.server == "" || c.tokenFile == "" || c.models == "") {
			logger.Print("service install needs -server, -token-file and -models (or DUMB_* env)")
			return exitUsage
		}
		if sub == "install" && sc.System && sc.GOOS == "linux" {
			logger.Printf("the system unit runs as user %q with the binary directory %s writable; create them first:\n%s",
				service.DefaultUser, filepath.Dir(bin), service.SystemUserHelp)
		}
		f := service.Install
		if sub == "uninstall" {
			f = service.Uninstall
		}
		if err := f(ctx, sc); err != nil {
			logger.Print(err)
			return exitError
		}
		fmt.Fprintf(stdout, "service %sed\n", sub)
		return exitOK
	}
	logger.Printf("unknown service command %q (install, uninstall, print)", sub)
	return exitUsage
}

func (c *config) addOllamaEnvironment(envs map[string]string) {
	if c.ollamaConfigured || c.ollamaURL != ollama.DefaultURL {
		envs["DUMB_OLLAMA"] = c.ollamaURL
	}
	if c.ollamaRemote {
		envs["DUMB_OLLAMA_REMOTE"] = "1"
	}
}

// ollamaClient opens the configured Ollama: loopback only, or a private-network
// host with the explicit remote opt-in.
func (c *config) ollamaClient() (*ollama.Client, error) {
	return ollama.Open(c.ollamaURL, c.ollamaRemote)
}
