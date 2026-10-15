//go:build !windows

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPersistsOnlyExplicitName(t *testing.T) {
	t.Setenv("DUMB_WORKER_NAME", "")
	f := newInstallFixture(t)
	unit := filepath.Join(f.home, ".config", "systemd", "user", "dumb-worker.service")
	if err := f.install("-server", "http://127.0.0.1:18080", "-models", "m", "-token-file", f.tokenFile("t")); err != nil {
		t.Fatal(err)
	}
	if u := read(t, unit); strings.Contains(u, "DUMB_WORKER_NAME") {
		t.Fatalf("implicit hostname persisted:\n%s", u)
	}
	if err := f.install("-server", "http://127.0.0.1:18080", "-models", "m", "-name", " Living room Pi 5 "); err != nil {
		t.Fatal(err)
	}
	if u := read(t, unit); !strings.Contains(u, `Environment="DUMB_WORKER_NAME=Living room Pi 5"`) {
		t.Fatalf("explicit name not persisted:\n%s", u)
	}
	t.Setenv("DUMB_WORKER_NAME", "From env")
	if env := installEnv(f.opts("-server", "http://127.0.0.1:18080", "-models", "m").cfg, "/tok"); env["DUMB_WORKER_NAME"] != "From env" {
		t.Fatalf("env name not carried: %v", env)
	}
	t.Setenv("DUMB_WORKER_NAME", "")
	if _, err := parseInstallFlags([]string{"-server", "http://127.0.0.1:18080", "-models", "m", "-name", "two\nlines"}, &strings.Builder{}); err == nil {
		t.Fatal("invalid name accepted by install")
	}
}
