//go:build windows

package main

import (
	"github.com/gkhntpbs/dumb/internal/windowstray"
	"os"
)

func main() {
	if windowstray.Run(os.Stdin, os.Stdout) != nil {
		os.Exit(1)
	}
}
