package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gkhntpbs/dumb/internal/branding"
)

func TestVersionPrintsBearThenVersionLine(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if !strings.HasPrefix(out.String(), branding.LogoCompact) || !strings.HasPrefix(lines[len(lines)-1], "dumb-worker ") {
		t.Fatalf("unexpected version output:\n%s", out.String())
	}
}
