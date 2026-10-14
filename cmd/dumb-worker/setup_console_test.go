package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/branding"
	"github.com/gkhntpbs/dumb/internal/worker"
)

func scriptedSetupRead(t *testing.T, values ...string) setupRead {
	t.Helper()
	return func(string, bool) (string, error) {
		if len(values) == 0 {
			t.Fatal("setup requested unexpected input")
			return "", errSetupExit
		}
		value := values[0]
		values = values[1:]
		return value, nil
	}
}

func TestSetupURLRetryAndAuthenticationRetryStayInFlowWithoutSecretOutput(t *testing.T) {
	var output bytes.Buffer
	display := &setupDisplay{output: &output}
	calls := 0
	read := scriptedSetupRead(t, "", "not-a-url", "http://127.0.0.1:19000", "private-invalid-token", "r", "http://127.0.0.1:19001", "private-valid-token")
	server, token, err := setupCredentials(read, display, func(value string) error { _, err := worker.CheckServerURL(value); return err }, func(server, token string) error {
		calls++
		if calls == 1 {
			return errors.New("worker credential check failed (HTTP 401)")
		}
		if server != "http://127.0.0.1:19001" || token != "private-valid-token" {
			t.Fatal("retry did not use corrected credentials")
		}
		return nil
	})
	if err != nil || calls != 2 || server != "http://127.0.0.1:19001" || token != "private-valid-token" {
		t.Fatal("retry failed", err)
	}
	if strings.Contains(output.String(), "private-") {
		t.Fatal("secret appeared in setup output")
	}
	for _, message := range []string{"A value is required", "Please correct this field", "HTTP 401", "window will remain open"} {
		if !strings.Contains(output.String(), message) {
			t.Fatal("missing recovery instruction", message)
		}
	}
}

func TestSetupConnectionFailureCanRetryOrExitWithoutReachingInstall(t *testing.T) {
	for _, choice := range []string{"e", "exit", ":exit"} {
		t.Run(choice, func(t *testing.T) {
			var output bytes.Buffer
			calls := 0
			_, _, err := setupCredentials(scriptedSetupRead(t, "http://127.0.0.1:19000", "private-token", choice), &setupDisplay{output: &output}, nil, func(string, string) error { calls++; return errors.New("server connection failed") })
			if !errors.Is(err, errSetupExit) || calls != 1 {
				t.Fatal("exit did not stop setup")
			}
			if strings.Contains(output.String(), "private-token") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestSetupSpinnerClampsToConsoleWidthAndStopsAfterOperation(t *testing.T) {
	var output bytes.Buffer
	display := &setupDisplay{output: &output, columns: func() int { return 40 }}
	if err := display.operation(strings.Repeat("download ", 20), func() error { time.Sleep(20 * time.Millisecond); return nil }); err != nil {
		t.Fatal(err)
	}
	rendered := strings.Split(output.String(), "\r")
	spinner := false
	for _, line := range rendered {
		if strings.Contains(line, "[elapsed ") {
			spinner = true
			if len([]rune(line)) >= 40 {
				t.Fatal("spinner wrapped beyond console width")
			}
		}
	}
	if !spinner || display.active {
		t.Fatal("spinner did not run and stop")
	}
}

func TestSetupBannerWidthAndNonInteractive(t *testing.T) {
	for _, tc := range []struct {
		columns int
		want    string
	}{{80, branding.Logo}, {56, branding.Logo}, {55, branding.LogoCompact}, {30, branding.LogoCompact}} {
		var out bytes.Buffer
		setupBanner(&out, tc.columns)
		if !strings.HasPrefix(out.String(), strings.TrimRight(tc.want, "\n")) {
			t.Fatalf("columns=%d: wrong logo", tc.columns)
		}
	}
	var out bytes.Buffer
	setupBanner(&out, 0)
	if out.Len() != 0 {
		t.Fatal("banner printed to non-terminal output")
	}
	if setupTerminalColumns(&out) != 0 {
		t.Fatal("buffer treated as terminal")
	}
	for _, logo := range []string{branding.Logo, branding.LogoCompact} {
		for _, line := range strings.Split(logo, "\n") {
			if len(line) >= setupBannerMinColumns || strings.ContainsAny(line, "\r\t") {
				t.Fatalf("logo line does not fit the minimum console width: %q", line)
			}
			for _, r := range line {
				if r > 0x7e {
					t.Fatalf("non-ASCII logo rune %q", r)
				}
			}
		}
	}
}
