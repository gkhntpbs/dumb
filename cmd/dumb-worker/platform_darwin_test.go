package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMacDesktopPrivateRootRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := macPrivateRoot(filepath.Join(link, "worker")); err == nil {
		t.Fatal("accepted symlink ancestor")
	}
	if _, err := os.Stat(filepath.Join(target, "worker")); !os.IsNotExist(err) {
		t.Fatal("modified linked target")
	}
}

func TestMacDesktopConfigPersistsRestrictedCredential(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "worker")
	if err := macPrivateRoot(root); err != nil {
		t.Fatal(err)
	}
	result := macSetupResult{Server: "https://example.invalid", Models: "test-model"}
	if err := macSave(root, result, "fixture-credential"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"token", "config.json"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s permissions %o", name, info.Mode().Perm())
		}
	}
	config, err := loadDesktopConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.server != result.Server || config.models != result.Models {
		t.Fatal("configuration mismatch")
	}
	if err := macSave(root, result, "replacement"); err == nil {
		t.Fatal("overwrote existing configuration")
	}
	token, _ := os.ReadFile(filepath.Join(root, "token"))
	if string(token) != "fixture-credential\n" {
		t.Fatal("credential changed")
	}
}

func TestMacDesktopRejectsMissingModelsBeforeWriting(t *testing.T) {
	root := t.TempDir()
	if err := macSave(root, macSetupResult{Server: "https://example.invalid"}, "fixture"); err == nil {
		t.Fatal("accepted missing models")
	}
	if _, err := os.Lstat(filepath.Join(root, "token")); !os.IsNotExist(err) {
		t.Fatal("wrote credential for invalid configuration")
	}
}

func TestMacDesktopRejectsSharedOrLinkedConfig(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "shared")
	if err := os.WriteFile(target, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := macLoadConfig(root); err == nil {
		t.Fatal("accepted linked configuration")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := macLoadConfig(root); err == nil {
		t.Fatal("accepted shared configuration")
	}
}

func TestMacDesktopCredentialChecksHTTPSIdentity(t *testing.T) {
	status := http.StatusOK
	body := `{"worker_id":"fixture-worker"}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workers/me" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("invalid credential request")
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer server.Close()
	ctx := context.Background()
	if err := macCredentialWith(ctx, server.Client(), server.URL, "fixture"); err != nil {
		t.Fatal(err)
	}
	status = http.StatusUnauthorized
	if err := macCredentialWith(ctx, server.Client(), server.URL, "fixture"); err == nil {
		t.Fatal("accepted rejected credential")
	}
	status = http.StatusOK
	body = `{}`
	if err := macCredentialWith(ctx, server.Client(), server.URL, "fixture"); err == nil {
		t.Fatal("accepted missing identity")
	}
	if err := macCredentialWith(ctx, server.Client(), "http://127.0.0.1:1", "fixture"); err == nil {
		t.Fatal("accepted plaintext setup")
	}
}

func TestMacDesktopRetryRetainsIssuedCredential(t *testing.T) {
	for _, failure := range []string{"verify", "save"} {
		t.Run(failure, func(t *testing.T) {
			exchanges, verifies, saves, retries, forms := 0, 0, 0, 0, 0
			actions := macSetupActions{
				dialog: func(_ context.Context, mode string, _ any, result any) error {
					var data []byte
					if mode == "--setup" {
						forms++
						data = []byte(`{"server":"https://example.invalid","models":"model","credential":"single-use-code","authentication":"pairing","start_at_login":true}`)
					} else if mode == "--retry" {
						retries++
						data = []byte(`{"retry":true}`)
					} else {
						t.Fatalf("unexpected dialog %s", mode)
					}
					return json.Unmarshal(data, result)
				},
				exchange: func(context.Context, string, string) (string, error) { exchanges++; return "issued-fixture", nil },
				verify: func(_ context.Context, _ string, token string) error {
					verifies++
					if token != "issued-fixture" {
						t.Fatal("lost issued credential")
					}
					if failure == "verify" && verifies == 1 {
						return errors.New("transient connection")
					}
					return nil
				},
				save: func(_ string, _ macSetupResult, token string) error {
					saves++
					if token != "issued-fixture" {
						t.Fatal("saved pairing code")
					}
					if failure == "save" && saves == 1 {
						return errors.New("transient storage")
					}
					return nil
				},
			}
			startup, err := macSetupWith(context.Background(), t.TempDir(), actions)
			if err != nil || !startup {
				t.Fatalf("startup=%v err=%v", startup, err)
			}
			if forms != 1 || exchanges != 1 || retries != 1 || verifies != 2 {
				t.Fatalf("forms=%d exchanges=%d retries=%d verifies=%d", forms, exchanges, retries, verifies)
			}
		})
	}
}
