package caller_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/server"
)

func TestTypeScriptCaller(t *testing.T) {
	if err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		_, err = os.ReadFile(path)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile("../../docs/protocol/openapi.yaml"); err != nil {
		t.Fatal(err)
	}

	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("TypeScript caller QA requires Node.js 22+ and npm; install the documented prerequisites")
	}
	if _, err := os.Stat("node_modules/typescript/bin/tsc"); err != nil {
		t.Fatal("TypeScript caller QA requires locked dev dependencies: npm ci --prefix clients/typescript")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	verify := exec.CommandContext(ctx, "npm", "run", "verify")
	output, err := verify.CombinedOutput()
	if err != nil {
		t.Fatalf("TypeScript caller verification: %v\n%s", err, output)
	}
	t.Log(string(output))
	secret := func() string {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(b)
	}
	workerToken, callerToken := secret(), secret()
	tokens := auth.NewStaticTokens()
	tokens.Add(secret(), auth.RoleWorker, workerToken)
	tokens.Add(secret(), auth.RoleCaller, callerToken)
	service, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	fixture := httptest.NewServer(service)
	defer fixture.Close()
	integration := exec.CommandContext(ctx, "node", "--test", "test/integration.test.mjs")
	integration.Env = append(os.Environ(), "QA_DUMB_URL="+fixture.URL, "QA_CALLER_TOKEN="+callerToken, "QA_WORKER_TOKEN="+workerToken)
	output, err = integration.CombinedOutput()
	if err != nil {
		t.Fatalf("TypeScript caller real Go server integration: %v\n%s", err, output)
	}
	t.Log(string(output))
}
