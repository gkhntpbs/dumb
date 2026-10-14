package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/worker"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPairRejectsInsecureURLBeforeExchange(t *testing.T) {
	_, e := exchangePair(context.Background(), http.DefaultClient, "http://127.0.0.1:1", strings.Repeat("a", 43))
	if e == nil {
		t.Fatal("insecure accepted")
	}
}
func TestPairAtomicTokenReplacement(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := restrictPairTestRoot(root); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "token")
	os.WriteFile(p, []byte("old"), 0600)
	if err := pairRestrict(p); err != nil {
		t.Fatal(err)
	}
	if e := storePairToken(p, "new"); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "new\n" {
		t.Fatal("replacement failed")
	}
	os.Remove(p)
	os.Symlink("other", p)
	if storePairToken(p, "bad") == nil {
		t.Fatal("symlink accepted")
	}
}

func TestPairCLIHTTPSFixture(t *testing.T) {
	code := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	raw := make([]byte, 32)
	raw[0] = 1
	secret := base64.RawURLEncoding.EncodeToString(raw)
	calls := 0
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/workers/pair" || r.Header.Get(protocol.WorkerVersionHeader) != version || r.Header.Get("Authorization") != "" {
			t.Error("invalid request")
		}
		var body struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Code != code {
			t.Error("invalid code")
		}
		json.NewEncoder(w).Encode(map[string]any{"token": secret, "credential": map[string]any{"credential_id": "fixture-c", "principal_id": "fixture-w", "role": "worker", "expires_at": time.Now().Add(time.Hour), "revoked": false}})
	}))
	defer fixture.Close()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	if err := restrictPairTestRoot(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "token")
	stdin, writer, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer stdin.Close()
	writer.WriteString(code + "\n")
	writer.Close()
	oldIn, oldClient := os.Stdin, http.DefaultClient
	os.Stdin, http.DefaultClient = stdin, fixture.Client()
	defer func() { os.Stdin, http.DefaultClient = oldIn, oldClient }()
	var out, errs bytes.Buffer
	if got := run(context.Background(), []string{"pair", "-server", fixture.URL, "-token-file", path}, &out, &errs); got != exitOK {
		t.Fatalf("CLI exit %d: %s", got, errs.String())
	}
	saved, e := worker.ReadTokenFile(path)
	if e != nil || saved != secret {
		t.Fatalf("saved credential: %v", e)
	}
	if strings.Contains(out.String()+errs.String(), code) || strings.Contains(out.String()+errs.String(), secret) {
		t.Fatal("secret disclosure")
	}
	if calls != 1 {
		t.Fatal("unexpected retry")
	}
}
func TestPairRejectsRedirectAndUnboundedResponse(t *testing.T) {
	code := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for _, status := range []int{302, 200, 401, 426, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/leak")
				w.WriteHeader(status)
				io.WriteString(w, strings.Repeat(code, 2000))
			}))
			defer s.Close()
			_, e := exchangePair(context.Background(), s.Client(), s.URL, code)
			if e == nil {
				t.Fatal("accepted response")
			}
			if strings.Contains(e.Error(), code) {
				t.Fatal("error disclosed code")
			}
			if calls != 1 {
				t.Fatal("followed redirect/retried")
			}
		})
	}
}
func TestPairFlagsNeverEchoSecret(t *testing.T) {
	secret := strings.Repeat("a", 43)
	for _, args := range [][]string{{"-code=" + secret}, {secret}, {"-server"}, {"-token=" + secret}} {
		var out, errs bytes.Buffer
		if run(context.Background(), append([]string{"pair"}, args...), &out, &errs) != exitUsage {
			t.Fatal("invalid flags accepted")
		}
		if strings.Contains(out.String()+errs.String(), secret) {
			t.Fatal("arguments disclosed")
		}
	}
}

func restrictPairTestRoot(root string) error {
	if runtime.GOOS == "windows" {
		return pairRestrict(root)
	}
	return nil
}
