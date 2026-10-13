//go:build !windows

package main

import (
	"context"
	"io"
)

func platformRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(ctx, args, stdout, stderr)
}

func platformReady(...desktopRuntimeInfo) {}
