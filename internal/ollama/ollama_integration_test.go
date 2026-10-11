package ollama

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestIntegration runs against the real local Ollama. It pulls a model but never
// deletes one. Enable with QA_OLLAMA=1; QA_OLLAMA_MODEL picks the model.
func TestIntegration(t *testing.T) {
	if os.Getenv("QA_OLLAMA") != "1" {
		t.Skip("set QA_OLLAMA=1 to run against a local Ollama")
	}
	model := os.Getenv("QA_OLLAMA_MODEL")
	if model == "" {
		model = "smollm2:135m"
	}
	c, err := New(os.Getenv("QA_OLLAMA_URL"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	v, err := c.Version(ctx)
	if err != nil || v == "" {
		t.Fatalf("version: %q %v", v, err)
	}
	t.Logf("ollama %s", v)

	last := ""
	err = c.Pull(ctx, model, func(p Progress) {
		if p.Status != last {
			t.Logf("pull: %s", p.Status)
			last = p.Status
		}
	})
	if err != nil || last != "success" {
		t.Fatalf("pull: %v (last status %q)", err, last)
	}

	names, err := c.List(ctx)
	if err != nil || !HasModel(names, model) {
		t.Fatalf("list: %v %v", names, err)
	}

	start := time.Now()
	out, err := c.Generate(ctx, GenerateRequest{Model: model, Prompt: "Say hello in one word."})
	if err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("generate: %q %v", out, err)
	}
	t.Logf("generate (%s): %q", time.Since(start).Round(time.Millisecond), out)
}
