// dumb-server queues AI jobs and hands them to workers that long-poll for them.
//
// Configuration (environment):
//
//	DUMB_ADDR                listen address (default 127.0.0.1:8080; put TLS in front of it)
//	DUMB_TOKENS_FILE         token table, one "<id> <worker|caller> <sha256-hex-of-token>" per line
//	DUMB_MIN_WORKER_VERSION  oldest worker version accepted (default 0.0.0)
//	DUMB_RECOMMENDED_WORKER_VERSION  version advertised to workers; older ones check their signed release source now (default none)
//	DUMB_ACK_WINDOW          time an ack-capable worker has to acknowledge an assignment (default 2s)
//	DUMB_LOG_LEVEL           debug, info (default), warn or error; logs are JSON on stderr
//	DUMB_RATE_WORKER         per-worker token bucket "<per-second>,<burst>" (default 50,100; "off" disables)
//	DUMB_RATE_CALLER         per-caller token bucket (default 50,100)
//	DUMB_RATE_UNAUTH         failed authentications per client IP (default 0.5,10)
//	DUMB_TRUSTED_PROXIES     comma-separated CIDRs whose X-Forwarded-For is believed (default none)
package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/queue"
	"github.com/gkhntpbs/dumb/internal/server"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0"

func main() {
	if err := run(); err != nil {
		log.Fatal("dumb-server: ", err)
	}
}

func run() (result error) {
	tokens, err := loadTokens(os.Getenv("DUMB_TOKENS_FILE"))
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("DUMB_LOG_LEVEL", "info"))); err != nil {
		return fmt.Errorf("DUMB_LOG_LEVEL: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	cfg := server.Config{Auth: tokens, MinWorkerVersion: env("DUMB_MIN_WORKER_VERSION", "0.0.0"),
		RecommendedWorkerVersion: os.Getenv("DUMB_RECOMMENDED_WORKER_VERSION"), Logger: logger}
	if cfg.QueueStateDirectory, cfg.QueueStateMaxBytes, err = queueStateFromEnvironment(); err != nil {
		return err
	}
	if cfg.QueueLimits, err = queueLimitsFromEnvironment(); err != nil {
		return err
	}
	if v := os.Getenv("DUMB_ACK_WINDOW"); v != "" {
		if cfg.AckWindow, err = time.ParseDuration(v); err != nil || cfg.AckWindow <= 0 {
			return fmt.Errorf("DUMB_ACK_WINDOW: %q: want a positive Go duration (e.g. 2s)", v)
		}
	}
	for _, r := range []struct {
		key string
		dst *server.Rate
	}{{"DUMB_RATE_WORKER", &cfg.WorkerRate}, {"DUMB_RATE_CALLER", &cfg.CallerRate}, {"DUMB_RATE_UNAUTH", &cfg.UnauthRate}} {
		if *r.dst, err = parseRate(os.Getenv(r.key)); err != nil {
			return fmt.Errorf("%s: %w", r.key, err)
		}
	}
	if cfg.TrustedProxies, err = parsePrefixes(os.Getenv("DUMB_TRUSTED_PROXIES")); err != nil {
		return fmt.Errorf("DUMB_TRUSTED_PROXIES: %w", err)
	}
	srv, err := server.New(cfg)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, srv.Close()) }()
	addr := env("DUMB_ADDR", "127.0.0.1:8080")
	logger.Info("dumb-server starting", "version", version, "protocol", protocol.Version, "addr", addr)
	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	if adminAddr := os.Getenv("DUMB_ADMIN_ADDR"); adminAddr != "" {
		listener, err := server.ListenAdmin(adminAddr)
		if err != nil {
			return err
		}
		defer listener.Close()
		adminServer := &http.Server{Handler: srv.AdminHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
		failures := make(chan error, 2)
		go func() { failures <- adminServer.Serve(listener) }()
		go func() { failures <- hs.ListenAndServe() }()
		defer hs.Close()
		defer adminServer.Close()
		err = <-failures
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	return hs.ListenAndServe()
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// parseRate reads "<per-second>,<burst>"; "" is the default, "off" disables.
func parseRate(v string) (server.Rate, error) {
	switch v {
	case "":
		return server.Rate{}, nil
	case "off":
		return server.Rate{PerSecond: -1}, nil
	}
	a, b, ok := strings.Cut(v, ",")
	r, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	burst, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if !ok || err1 != nil || err2 != nil || r <= 0 || burst < 1 {
		return server.Rate{}, fmt.Errorf("%q: want \"<per-second>,<burst>\" (e.g. 50,100) or off", v)
	}
	return server.Rate{PerSecond: r, Burst: burst}, nil
}

func parsePrefixes(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			a, aerr := netip.ParseAddr(f)
			if aerr != nil {
				return nil, err
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func loadTokens(path string) (*auth.StaticTokens, error) {
	if path == "" {
		return nil, fmt.Errorf("DUMB_TOKENS_FILE is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tokens := auth.NewStaticTokens()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 3 || (fs[1] != string(auth.RoleWorker) && fs[1] != string(auth.RoleCaller) && fs[1] != string(auth.RoleAdminRead)) || len(fs[2]) != 64 {
			return nil, fmt.Errorf("%s:%d: want \"<id> <worker|caller|admin-read> <sha256-hex>\"", path, n)
		}
		tokens.AddHash(fs[0], auth.Role(fs[1]), strings.ToLower(fs[2]))
	}
	return tokens, sc.Err()
}

func queueLimitsFromEnvironment() (queue.Limits, error) {
	var limits queue.Limits
	for _, field := range []struct {
		key   string
		value *int
	}{
		{"DUMB_QUEUE_MAX_ACTIVE_JOBS", &limits.MaxActiveJobs},
		{"DUMB_QUEUE_MAX_JOBS", &limits.MaxJobs},
		{"DUMB_QUEUE_MAX_BYTES", &limits.MaxBytes},
		{"DUMB_QUEUE_CALLER_MAX_ACTIVE_JOBS", &limits.CallerMaxActiveJobs},
		{"DUMB_QUEUE_CALLER_MAX_JOBS", &limits.CallerMaxJobs},
		{"DUMB_QUEUE_CALLER_MAX_BYTES", &limits.CallerMaxBytes},
	} {
		if value, set := os.LookupEnv(field.key); set {
			if value == "" || strings.Trim(value, "0123456789") != "" {
				return limits, fmt.Errorf("%s: want a positive decimal integer", field.key)
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed <= 0 {
				return limits, fmt.Errorf("%s: want a positive decimal integer", field.key)
			}
			*field.value = parsed
		}
	}
	return limits.Normalize()
}

func queueStateFromEnvironment() (string, int64, error) {
	directory := os.Getenv("DUMB_QUEUE_STATE_DIRECTORY")
	var maxBytes int64
	if value, set := os.LookupEnv("DUMB_QUEUE_STATE_MAX_BYTES"); set {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 || strings.Trim(value, "0123456789") != "" {
			return "", 0, errors.New("DUMB_QUEUE_STATE_MAX_BYTES: want a positive decimal integer")
		}
		maxBytes = parsed
	}
	if directory == "" && maxBytes != 0 {
		return "", 0, errors.New("DUMB_QUEUE_STATE_DIRECTORY is required with DUMB_QUEUE_STATE_MAX_BYTES")
	}
	return directory, maxBytes, nil
}
