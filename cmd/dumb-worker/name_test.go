package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNameFlagAndEnvironment(t *testing.T) {
	t.Setenv("DUMB_WORKER_NAME", "")
	c, _, err := parseFlags("run", nil, &bytes.Buffer{})
	if err != nil || c.name != "" {
		t.Fatalf("default name %q (%v): the hostname is chosen at run time, not stored", c.name, err)
	}
	t.Setenv("DUMB_WORKER_NAME", "  from env ")
	if c, _, err = parseFlags("run", nil, &bytes.Buffer{}); err != nil || c.name != "from env" {
		t.Fatalf("env name %q (%v)", c.name, err)
	}
	if c, _, err = parseFlags("run", []string{"-name", "from flag"}, &bytes.Buffer{}); err != nil || c.name != "from flag" {
		t.Fatalf("flag name %q (%v)", c.name, err)
	}
	t.Setenv("DUMB_WORKER_NAME", "")
	for _, bad := range []string{"   ", "two\nlines", "esc\x1b[0m", strings.Repeat("x", 65)} {
		if _, _, err := parseFlags("run", []string{"-name", bad}, &bytes.Buffer{}); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	if c, _, err = parseFlags("run", []string{"-name", strings.Repeat("ç", 64)}, &bytes.Buffer{}); err != nil {
		t.Fatalf("64-character name refused: %v", err)
	}
	if code := run(context.Background(), []string{"run", "-name", "bad\tname"}, &bytes.Buffer{}, &bytes.Buffer{}); code != exitUsage {
		t.Fatalf("bad name exit %d, want usage", code)
	}
}

// service print shows the name only when one was given explicitly; without
// it the installed service keeps following the hostname.
func TestServiceCarriesOnlyExplicitName(t *testing.T) {
	t.Setenv("DUMB_WORKER_NAME", "")
	print := func(args ...string) string {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), append([]string{"service", "print", "-server", "https://dumb.example.invalid", "-models", "m"}, args...), &out, &errOut); code != exitOK {
			t.Fatalf("service print %v: %d %s", args, code, errOut.String())
		}
		return out.String()
	}
	if got := print(); strings.Contains(got, "DUMB_WORKER_NAME") || strings.Contains(got, "-name") {
		t.Fatalf("implicit hostname persisted:\n%s", got)
	}
	// DUMB_WORKER_NAME in a plist or unit; -name in the Windows sc.exe binPath.
	if got := print("-name", " Lab Pi "); !(strings.Contains(got, "DUMB_WORKER_NAME") || strings.Contains(got, "-name")) || !strings.Contains(got, "Lab Pi") || strings.Contains(got, " Lab Pi ") {
		t.Fatalf("explicit name missing or untrimmed:\n%s", got)
	}
}

func TestDesktopConfigKeepsExplicitName(t *testing.T) {
	t.Setenv("DUMB_OLLAMA_DATA_DIR", "")
	t.Setenv("OLLAMA_MODELS", "")
	t.Setenv("DUMB_OLLAMA", "")
	t.Setenv("DUMB_WORKER_NAME", "")
	saved, err := desktopSetupConfig("http://127.0.0.1:18080", "fixture:model")
	if err != nil || saved.Name != "" {
		t.Fatalf("implicit name saved: %q (%v)", saved.Name, err)
	}
	t.Setenv("DUMB_WORKER_NAME", "bad\nname")
	if _, err := desktopSetupConfig("http://127.0.0.1:18080", "fixture:model"); err == nil {
		t.Fatal("invalid name accepted at setup")
	}
	t.Setenv("DUMB_WORKER_NAME", " Desk PC ")
	if saved, err = desktopSetupConfig("http://127.0.0.1:18080", "fixture:model"); err != nil || saved.Name != "Desk PC" {
		t.Fatalf("setup name %q (%v)", saved.Name, err)
	}
	root := desktopTestRoot(t)
	write := func(v desktopConfig) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(saved)
	if err := os.WriteFile(filepath.Join(root, "token"), []byte("inert-token"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUMB_WORKER_NAME", "")
	if c, err := loadDesktopConfig(root); err != nil || c.name != "Desk PC" {
		t.Fatalf("relaunch name %q (%v)", c.name, err)
	}
	t.Setenv("DUMB_WORKER_NAME", "Launch override")
	if c, err := loadDesktopConfig(root); err != nil || c.name != "Launch override" {
		t.Fatalf("launch name lost precedence: %q (%v)", c.name, err)
	}
	t.Setenv("DUMB_WORKER_NAME", "")
	saved.Name = "tampered\x00name"
	write(saved)
	if _, err := loadDesktopConfig(root); err == nil {
		t.Fatal("invalid saved name accepted")
	}
}
