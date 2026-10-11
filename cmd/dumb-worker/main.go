// dumb-worker pulls jobs from dumb-server and runs them on the local Ollama.
// It accepts no inbound connections.
//
// Usage:
//
//	dumb-worker [run]                         serve jobs (default)
//	dumb-worker doctor                        check this machine and the configuration
//	dumb-worker setup                         install/start Ollama and pull the models
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
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/gkhntpbs/dumb/internal/worker"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0"

// Exit codes.
const (
	exitOK      = 0
	exitError   = 1
	exitUsage   = 2
	exitUpgrade = 3 // the server requires a newer worker
)

func main() {
	if _, err := protocol.ParseSemVer(version); err != nil {
		fmt.Fprintln(os.Stderr, "dumb-worker: invalid build version:", err)
		os.Exit(exitError)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type config struct {
	server, tokenFile, models, ollamaURL string
	capacity                             int
	system                               bool // service: Linux system unit
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
	c := &config{}
	fs := flag.NewFlagSet("dumb-worker "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	capDefault, err := strconv.Atoi(env("DUMB_CAPACITY", "1"))
	if err != nil {
		return nil, nil, fmt.Errorf("DUMB_CAPACITY: %w", err)
	}
	fs.StringVar(&c.server, "server", os.Getenv("DUMB_SERVER"), "server URL (env DUMB_SERVER)")
	fs.StringVar(&c.tokenFile, "token-file", os.Getenv("DUMB_TOKEN_FILE"), "token file, mode 0600 (env DUMB_TOKEN_FILE)")
	fs.StringVar(&c.models, "models", os.Getenv("DUMB_MODELS"), "comma-separated models (env DUMB_MODELS)")
	fs.IntVar(&c.capacity, "capacity", capDefault, "concurrent jobs (env DUMB_CAPACITY)")
	fs.StringVar(&c.ollamaURL, "ollama", env("DUMB_OLLAMA", ollama.DefaultURL), "Ollama URL, loopback only (env DUMB_OLLAMA)")
	if name == "service" {
		fs.BoolVar(&c.system, "system", false, "Linux: install a system unit instead of a user unit")
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
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
		fmt.Fprintf(stdout, "dumb-worker %s (protocol v%d)\n", version, protocol.Version)
		return exitOK
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
	fmt.Fprintf(stderr, "dumb-worker: unknown command %q (run, doctor, setup, service, version)\n", cmd)
	return exitUsage
}

func cmdRun(ctx context.Context, c *config, logger *log.Logger) int {
	token, err := worker.ReadTokenFile(c.tokenFile)
	if err != nil {
		logger.Print(err)
		return exitError
	}
	w, err := worker.New(worker.Config{
		ServerURL: c.server, Token: token, Models: c.modelList(), Capacity: c.capacity,
		OllamaURL: c.ollamaURL, Version: version, Logger: logger,
	})
	if err != nil {
		logger.Print(err)
		return exitError
	}
	logger.Printf("version %s, server %s", version, c.server)
	err = w.Run(ctx)
	var up *worker.ErrUpgradeRequired
	switch {
	case errors.As(err, &up):
		// TODO(updates): hand over to the self-updater here; see TODO.md "Updates / release".
		logger.Printf("UPGRADE REQUIRED: the server accepts version %s or newer; this is %s. Stopping.", up.MinVersion, version)
		return exitUpgrade
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		logger.Print("stopped")
		return exitOK
	case err != nil:
		logger.Print(err)
		return exitError
	}
	return exitOK
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
	oc, err := ollama.New(c.ollamaURL)
	if err != nil {
		pre = append(pre, doctor.Result{Name: "Ollama URL", Detail: err.Error()})
		doctor.Print(stdout, pre)
		return exitError
	}
	d := &doctor.Doctor{
		ServerURL: c.server, Token: token, Version: version, Models: c.modelList(), OllamaPort: oc.Port(),
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
	oc, err := ollama.New(c.ollamaURL)
	if err != nil {
		logger.Print(err)
		return exitError
	}
	sys := ollama.NewSystem(oc)
	d := sys.Detect(ctx)
	if !d.Installed {
		fmt.Fprintln(stdout, "Ollama not found; installing it the official way...")
		if err := sys.Install(ctx); err != nil {
			logger.Print("install: ", err)
			return exitError
		}
	} else {
		fmt.Fprintf(stdout, "Ollama found: %s\n", strings.TrimSpace(d.Path+" "+d.Version))
	}
	if !d.Reachable {
		fmt.Fprintln(stdout, "Starting Ollama...")
		if err := sys.Start(ctx); err != nil {
			logger.Print("start: ", err)
			return exitError
		}
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	v, err := sys.WaitReachable(wctx, time.Second)
	cancel()
	if err != nil {
		logger.Print(err)
		return exitError
	}
	fmt.Fprintf(stdout, "Ollama %s is reachable on loopback.\n", v)
	for _, m := range c.modelList() {
		fmt.Fprintf(stdout, "Pulling %s...\n", m)
		lastPct, lastStatus := -1, ""
		err := oc.Pull(ctx, m, func(p ollama.Progress) {
			if p.Total > 0 {
				if pct := int(p.Completed * 100 / p.Total); pct/10 != lastPct/10 {
					fmt.Fprintf(stdout, "  %s %d%%\n", p.Status, pct)
					lastPct = pct
				}
				return
			}
			if p.Status != lastStatus {
				fmt.Fprintf(stdout, "  %s\n", p.Status)
				lastStatus, lastPct = p.Status, -1
			}
		})
		if err != nil {
			logger.Print(err)
			return exitError
		}
	}
	fmt.Fprintln(stdout, "Setup complete. Next: dumb-worker doctor")
	return exitOK
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
	if c.ollamaURL != ollama.DefaultURL {
		envs["DUMB_OLLAMA"] = c.ollamaURL
	}
	home, _ := os.UserHomeDir()
	sc := service.Config{
		GOOS: runtime.GOOS, Binary: bin, Env: envs, Home: home, UID: os.Getuid(),
		System: c.system, Runner: ollama.ExecRunner{},
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
		fmt.Fprint(stdout, s)
		return exitOK
	case "install", "uninstall":
		if sub == "install" && (c.server == "" || c.tokenFile == "" || c.models == "") {
			logger.Print("service install needs -server, -token-file and -models (or DUMB_* env)")
			return exitUsage
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
