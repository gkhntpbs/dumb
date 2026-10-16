package menubar

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("QA_MENU_CHILD"); mode != "" {
		if mode == "arguments" && (len(os.Args) != 2 || os.Args[1] != "internal-tray") {
			os.Exit(2)
		}
		if mode == "silent" {
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		if mode == "mismatch" {
			fmt.Println(`{"version":2,"event":"ready"}`)
			time.Sleep(time.Minute)
			os.Exit(0)
		}
		fmt.Println(`{"version":1,"event":"ready"}`)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var status Status
			if json.Unmarshal(scanner.Bytes(), &status) != nil || !status.Valid() {
				os.Exit(3)
			}
			if mode == "die" {
				os.Exit(0)
			}
			fmt.Println(`{"version":1,"event":"pause"}`)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSessionHandshakeControlAndClose(t *testing.T) {
	t.Setenv("QA_MENU_CHILD", "valid")
	exe, _ := os.Executable()
	session, err := Start(context.Background(), exe, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.Update(Status{Version: 1, State: "serving", Active: 2, Category: "none"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-session.Events():
		if event != "pause" {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("control missing")
	}
	if err := session.Update(Status{Version: 1, State: "secret", Category: "none"}); err == nil {
		t.Fatal("invalid status accepted")
	}
}

func TestSessionFailsClosed(t *testing.T) {
	for _, mode := range []string{"mismatch", "silent", "missing", "die"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("QA_MENU_CHILD", mode)
			// The helper child is this test binary. Under -race its runtime
			// sleeps GORACE atexit_sleep_ms (default 1s) before exiting, which
			// alone would exceed the detection window below.
			t.Setenv("GORACE", "atexit_sleep_ms=0")
			exe, _ := os.Executable()
			if mode == "missing" {
				exe = t.TempDir() + "/missing"
			}
			session, err := Start(context.Background(), exe, 50*time.Millisecond)
			if mode != "die" {
				if err == nil {
					session.Close()
					t.Fatal("invalid helper accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			session.Update(Status{Version: 1, State: "ready", Category: "none"})
			select {
			case <-session.Failed():
			case <-time.After(time.Second):
				t.Fatal("death not detected")
			}
		})
	}
}

func TestNativeHelperProtocol(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AppKit is macOS-only")
	}
	source, err := os.ReadFile("../../macos/MenuHelper.swift")
	if err != nil {
		t.Fatal(err)
	}
	if len(source) == 0 {
		t.Fatal("native source missing")
	}
	compiler, err := exec.LookPath("swiftc")
	if err != nil {
		t.Fatal("swiftc is required for macOS menu QA")
	}
	binary := filepath.Join(t.TempDir(), "dumb-menu")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, compiler, "-swift-version", "5", "-framework", "AppKit", "../../macos/MenuHelper.swift", "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("native compile: %v: %s", err, output)
	}
	if output, err := exec.CommandContext(ctx, binary, "--self-test").CombinedOutput(); err != nil {
		t.Fatalf("native protocol: %v: %s", err, output)
	}
	if output, err := exec.CommandContext(ctx, binary, "--setup-self-test").CombinedOutput(); err != nil {
		t.Fatalf("native setup protocol: %v: %s", err, output)
	}
	for _, test := range []struct {
		name  string
		input string
		valid bool
	}{
		{name: "valid_eof", input: `{"server":"https://example.invalid","models":"model","error":""}`, valid: true},
		{name: "valid_newline_eof", input: "{}\n", valid: true},
		{name: "exact_limit", input: `{"error":"` + strings.Repeat("x", 16372) + `"}`, valid: true},
		{name: "over_limit", input: `{"error":"` + strings.Repeat("x", 16373) + `"}`},
		{name: "empty_eof"},
		{name: "malformed", input: "{"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(ctx, binary, "--setup-input-self-test")
			command.Stdin = strings.NewReader(test.input)
			output, err := command.CombinedOutput()
			if test.valid {
				if err != nil || string(output) != "Native setup input passed\n" {
					t.Fatalf("native setup input rejected: %v: %s", err, output)
				}
			} else if err == nil {
				t.Fatalf("native setup input accepted: %s", output)
			}
		})
	}
}

func TestSessionHelperArguments(t *testing.T) {
	t.Setenv("QA_MENU_CHILD", "arguments")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	session, err := StartWithArgs(context.Background(), exe, []string{"internal-tray"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	session.Close()
}
