// dumb-server queues AI jobs and hands them to workers that long-poll for them.
//
// Configuration (environment):
//
//	DUMB_ADDR                listen address (default 127.0.0.1:8080; put TLS in front of it)
//	DUMB_TOKENS_FILE         token table, one "<id> <worker|caller> <sha256-hex-of-token>" per line
//	DUMB_MIN_WORKER_VERSION  oldest worker version accepted (default 0.0.0)
package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/server"
)

func main() {
	if err := run(); err != nil {
		log.Fatal("dumb-server: ", err)
	}
}

func run() error {
	tokens, err := loadTokens(os.Getenv("DUMB_TOKENS_FILE"))
	if err != nil {
		return err
	}
	srv, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: env("DUMB_MIN_WORKER_VERSION", "0.0.0")})
	if err != nil {
		return err
	}
	addr := env("DUMB_ADDR", "127.0.0.1:8080")
	log.Printf("dumb-server: protocol v%d listening on %s", protocol.Version, addr)
	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	return hs.ListenAndServe()
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
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
		if len(fs) != 3 || (fs[1] != string(auth.RoleWorker) && fs[1] != string(auth.RoleCaller)) || len(fs[2]) != 64 {
			return nil, fmt.Errorf("%s:%d: want \"<id> <worker|caller> <sha256-hex>\"", path, n)
		}
		tokens.AddHash(fs[0], auth.Role(fs[1]), strings.ToLower(fs[2]))
	}
	return tokens, sc.Err()
}
