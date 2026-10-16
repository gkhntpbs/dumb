package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func main() {
	version := flag.String("version", "0.0.0", "existing build version (no automatic bump)")
	flag.Parse()
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`).MatchString(*version) {
		fmt.Fprintln(os.Stderr, "invalid release version")
		os.Exit(1)
	}
	for _, target := range []struct{ command, description string }{
		{"dumb-worker", "DUMB Worker"},
		{"dumb-tray", "DUMB Tray"},
	} {
		cmd := exec.Command("go", "run", "github.com/tc-hib/go-winres@v0.3.3", "simply",
			"--arch", "amd64", "--out", "cmd/"+target.command+"/rsrc",
			"--icon", "internal/branding/assets/tray.ico", "--manifest", "gui",
			"--file-version", *version, "--product-version", *version,
			"--file-description", target.description, "--product-name", "DUMB",
			"--original-filename", target.command+".exe")
		for _, env := range os.Environ() {
			key, _, _ := strings.Cut(env, "=")
			if key != "GOOS" && key != "GOARCH" && key != "CGO_ENABLED" {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
