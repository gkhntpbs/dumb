package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/recoverylab"
)

func TestRecoveryConfiguredEndpointProvenance(t *testing.T) {
	for _, scenario := range []struct {
		name, environment string
		arguments         []string
		external          bool
	}{
		{"implicit", "", nil, false},
		{"explicit_default_flag", "", []string{"-ollama", ollama.DefaultURL}, true},
		{"explicit_default_environment", ollama.DefaultURL, nil, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("DUMB_OLLAMA", scenario.environment)
			c, _, err := parseFlags("run", scenario.arguments, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if c.ollamaConfigured != scenario.external {
				t.Fatal("endpoint provenance lost")
			}
			env := map[string]string{}
			c.addOllamaEnvironment(env)
			_, present := env["DUMB_OLLAMA"]
			if present != scenario.external {
				t.Fatal("service environment changed endpoint ownership")
			}
		})
	}
}

func TestRecoverySetupUsesSealedSystemAndStopsOnlyOwnedChild(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "external"}[external], func(t *testing.T) {
			fixture := recoverylab.New(t)
			if external {
				fixture.SetRunning(true)
				fixture.AddModel("fixture")
			}
			c := &config{models: "fixture", ollamaURL: ollama.DefaultURL, ollamaConfigured: external, newSystem: func(*ollama.Client) *ollama.System { return fixture.System() }}
			output := &bytes.Buffer{}
			if code := cmdSetup(context.Background(), c, output, log.New(io.Discard, "", 0)); code != exitOK {
				t.Fatalf("setup exit=%d", code)
			}
			if !strings.Contains(output.String(), "dumb-worker run") {
				t.Fatal("setup did not explain runtime ownership")
			}
			snapshot := fixture.Snapshot()
			if external {
				if snapshot.Starts+snapshot.Stops+snapshot.Downloads != 0 {
					t.Fatal("setup touched external endpoint")
				}
			} else {
				if snapshot.Starts != 1 || snapshot.Stops != 1 || snapshot.Downloads != 1 {
					t.Fatalf("owned setup lifecycle: %+v", snapshot)
				}
			}
		})
	}
}
