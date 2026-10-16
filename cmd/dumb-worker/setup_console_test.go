package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	t.Setenv("DUMB_SERVER", "")
	var output bytes.Buffer
	display := &setupDisplay{output: &output}
	calls := 0
	read := scriptedSetupRead(t, "", "not-a-url", "http://127.0.0.1:19000", "token", "private-invalid-token", "r", "http://127.0.0.1:19001", "token", "private-valid-token")
	server, token, err := setupCredentials(read, display, func(value string) error { _, err := worker.CheckServerURL(value); return err }, nil, func(server, token string) error {
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
	t.Setenv("DUMB_SERVER", "")
	for _, choice := range []string{"e", "exit", ":exit"} {
		t.Run(choice, func(t *testing.T) {
			var output bytes.Buffer
			calls := 0
			_, _, err := setupCredentials(scriptedSetupRead(t, "http://127.0.0.1:19000", "token", "private-token", choice), &setupDisplay{output: &output}, nil, nil, func(string, string) error { calls++; return errors.New("server connection failed") })
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

func TestSetupPairingExchangesCodeBeforeCheckingCredential(t *testing.T) {
	t.Setenv("DUMB_SERVER", "")
	var output bytes.Buffer
	exchanges, checks := 0, 0
	server, token, err := setupCredentials(scriptedSetupRead(t, "https://example.test", "pair", "private-code", "r"), &setupDisplay{output: &output}, nil, func(server, code string) (string, error) {
		exchanges++
		if server != "https://example.test" || code != "private-code" {
			t.Fatal("incorrect exchange input")
		}
		return "private-issued-token", nil
	}, func(server, token string) error {
		checks++
		if token != "private-issued-token" {
			t.Fatal("pairing code used as bearer credential")
		}
		if checks == 1 {
			return errors.New("server connection failed")
		}
		return nil
	})
	if err != nil || server != "https://example.test" || token != "private-issued-token" || exchanges != 1 || checks != 2 {
		t.Fatalf("exchange/check retry failed: exchanges=%d checks=%d error=%v", exchanges, checks, err)
	}
	if strings.Contains(output.String(), "private-") {
		t.Fatal("secret leaked")
	}
}

func TestSetupPairingFailureRequiresFreshCodeAndExplicitRetry(t *testing.T) {
	t.Setenv("DUMB_SERVER", "")
	var output bytes.Buffer
	exchanges, checks := 0, 0
	_, token, err := setupCredentials(scriptedSetupRead(t, "https://example.test", "pair", "expired-code", "r", "https://example.test", "pair", "fresh-code"), &setupDisplay{output: &output}, nil, func(server, code string) (string, error) {
		exchanges++
		if exchanges == 1 {
			return "", errors.New("pair failed (HTTP 401); no automatic retry")
		}
		if code != "fresh-code" {
			t.Fatal("consumed code retried")
		}
		return "issued-token", nil
	}, func(string, string) error { checks++; return nil })
	if err != nil || token != "issued-token" || exchanges != 2 || checks != 1 {
		t.Fatal("fresh-code recovery failed", err)
	}
}

func TestSetupPairingHTTPSExchange(t *testing.T) {
	t.Setenv("DUMB_SERVER", "")
	code := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	raw := make([]byte, 32)
	raw[0] = 1
	issued := base64.RawURLEncoding.EncodeToString(raw)
	calls := 0
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workers/pair" || r.Header.Get("Authorization") != "" {
			t.Error("invalid pairing request")
		}
		var request struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Code != code {
			t.Error("invalid pairing code")
		}
		json.NewEncoder(w).Encode(map[string]any{"token": issued, "credential": map[string]any{"credential_id": "fixture-c", "principal_id": "fixture-w", "role": "worker", "expires_at": time.Now().Add(time.Hour), "revoked": false}})
	}))
	defer fixture.Close()
	var output bytes.Buffer
	_, token, err := setupCredentials(scriptedSetupRead(t, fixture.URL, "", code), &setupDisplay{output: &output}, nil, func(server, code string) (string, error) {
		return exchangePair(context.Background(), fixture.Client(), server, code)
	}, func(server, token string) error {
		if token != issued {
			t.Fatal("pairing code used as bearer credential")
		}
		return nil
	})
	if err != nil || token != issued || calls != 1 {
		t.Fatal("HTTPS pairing failed", err)
	}
	if strings.Contains(output.String(), code) || strings.Contains(output.String(), issued) {
		t.Fatal("credential leaked")
	}
}

func TestSetupDefaultsRequireConfirmationAndAllowOverride(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"", "https://example.test"}, {"https://other.test", "https://other.test"}, {":exit", ":exit"}} {
		read := setupDefaultRead(func(label string, secret bool) (string, error) {
			if label != "Server URL [https://example.test]" || secret {
				t.Fatal("default was not visibly confirmed")
			}
			return tc.input, nil
		}, "Server URL", "https://example.test")
		got, err := read("Server URL", false)
		if err != nil || got != tc.want {
			t.Fatal("incorrect default confirmation", err)
		}
	}
}
