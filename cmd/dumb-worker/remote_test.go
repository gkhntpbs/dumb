package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOllamaRemoteFlag(t *testing.T) {
	c, _, err := parseFlags("run", []string{"-ollama-remote", "-ollama", "http://192.168.10.20:11434"}, &bytes.Buffer{})
	if err != nil || !c.ollamaRemote {
		t.Fatalf("flag: %v, remote %v", err, c != nil && c.ollamaRemote)
	}
	if cl, err := c.ollamaClient(); err != nil || !cl.Remote() {
		t.Fatalf("private URL with opt-in: %v", err)
	}
	t.Setenv("DUMB_OLLAMA_REMOTE", "1")
	if c, _, _ = parseFlags("run", nil, &bytes.Buffer{}); !c.ollamaRemote {
		t.Fatal("DUMB_OLLAMA_REMOTE=1 ignored")
	}
	envs := map[string]string{}
	c.addOllamaEnvironment(envs)
	if envs["DUMB_OLLAMA_REMOTE"] != "1" {
		t.Fatalf("service environment %v lacks DUMB_OLLAMA_REMOTE", envs)
	}
}

// Without the opt-in a private-network URL is refused exactly as before; with
// it, a public or loopback URL is still refused. Doctor reports it and stops.
func TestOllamaRemoteURLRefusals(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-ollama", "http://192.168.10.20:11434"}, "not a loopback address"},
		{[]string{"-ollama-remote", "-ollama", "http://8.8.8.8:11434"}, "not on a private network"},
		{[]string{"-ollama-remote", "-ollama", "http://127.0.0.1:11434"}, "drop -ollama-remote"},
		{[]string{"-ollama-remote"}, "drop -ollama-remote"}, // the default URL is loopback
	} {
		var out, errOut bytes.Buffer
		args := append([]string{"doctor", "-server", "http://127.0.0.1:1", "-models", "m"}, tc.args...)
		if code := run(context.Background(), args, &out, &errOut); code != exitError || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%v: exit %d, output %q, want %q", tc.args, code, out.String(), tc.want)
		}
	}
}
