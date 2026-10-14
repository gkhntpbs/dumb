package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gkhntpbs/dumb/internal/branding"
)

// setupBannerMinColumns is the narrowest console that gets the full logo.
const setupBannerMinColumns = 56

// setupBanner prints the DUMB bear for an interactive setup. columns <= 0
// means the output is not a terminal (pipe, file, log), so nothing is printed.
func setupBanner(w io.Writer, columns int) {
	if columns <= 0 {
		return
	}
	logo := branding.LogoCompact
	if columns >= setupBannerMinColumns {
		logo = branding.Logo
	}
	fmt.Fprintln(w, strings.TrimRight(logo, "\n"))
	fmt.Fprintln(w)
}

// setupTerminalColumns reports the width of w when it is a terminal, or 0.
func setupTerminalColumns(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return 0
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 80
}

var errSetupExit = errors.New("setup closed by user")

type setupRead func(string, bool) (string, error)

type setupDisplay struct {
	output  io.Writer
	mu      sync.Mutex
	label   string
	started time.Time
	active  bool
	frame   int
	columns func() int
}

func (d *setupDisplay) width() int {
	if d.columns != nil {
		if width := d.columns(); width > 20 {
			return width
		}
	}
	return 80
}

func (d *setupDisplay) clear() { fmt.Fprint(d.output, "\r", strings.Repeat(" ", d.width()-1), "\r") }

func (d *setupDisplay) note(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active {
		d.clear()
	}
	fmt.Fprintf(d.output, format+"\n", args...)
}

func (d *setupDisplay) set(label string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.label = label
}

func (d *setupDisplay) operation(label string, execute func() error) error {
	d.mu.Lock()
	d.label, d.started, d.active = label, time.Now(), true
	d.mu.Unlock()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			d.mu.Lock()
			d.clear()
			line := fmt.Sprintf("%c %s  [elapsed %s]", "|/-\\"[d.frame%4], d.label, time.Since(d.started).Truncate(time.Second))
			if len(line) >= d.width() {
				elapsed := fmt.Sprintf(" [elapsed %s]", time.Since(d.started).Truncate(time.Second))
				label := []rune(d.label)
				limit := d.width() - len(elapsed) - 4
				if len(label) > limit {
					label = label[:limit]
				}
				line = fmt.Sprintf("%c %s%s", "|/-\\"[d.frame%4], string(label), elapsed)
			}
			fmt.Fprint(d.output, line)
			d.frame++
			d.mu.Unlock()
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	err := execute()
	close(done)
	<-finished
	d.mu.Lock()
	d.clear()
	d.active = false
	d.mu.Unlock()
	if err == nil {
		d.note("OK - %s", label)
	}
	return err
}

func setupValidated(read setupRead, display *setupDisplay, label string, secret bool, validate func(string) error) (string, error) {
	for {
		value, err := read(label, secret)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(value)
		if strings.EqualFold(value, ":exit") {
			return "", errSetupExit
		}
		if value == "" {
			display.note("A value is required. Paste or type it, then press Enter.")
			continue
		}
		if validate != nil {
			if err := validate(value); err != nil {
				display.note("Please correct this field: %v", err)
				continue
			}
		}
		return value, nil
	}
}

func setupRetry(read setupRead, display *setupDisplay, err error) bool {
	display.note("\nSetup has paused: %v", err)
	display.note("Nothing else will run until you choose Retry or Exit.")
	for {
		value, readErr := read("Retry or Exit [R/E]", false)
		if readErr != nil {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "r", "retry":
			return true
		case "e", "exit", ":exit":
			return false
		default:
			display.note("Enter R to retry or E to exit.")
		}
	}
}

func setupCredentials(read setupRead, display *setupDisplay, validateURL func(string) error, check func(string, string) error) (string, string, error) {
	for {
		display.note("\n[1/7] Server connection")
		display.note("Enter your server's HTTPS URL. Local tests may use http://127.0.0.1:PORT.")
		server, err := setupValidated(read, display, "Server URL", false, validateURL)
		if err != nil {
			return "", "", err
		}
		display.note("\n[2/7] Worker authentication")
		display.note("Copy your worker token. Ctrl+V or right-click pastes; input is masked.")
		token, err := setupValidated(read, display, "Pre-issued worker token (hidden)", true, nil)
		if err != nil {
			return "", "", err
		}
		err = display.operation("Checking server credentials and worker version", func() error { return check(server, token) })
		if err == nil {
			return server, token, nil
		}
		if !setupRetry(read, display, err) {
			return "", "", errSetupExit
		}
		display.note("Enter the server URL and token again. The window will remain open.")
	}
}
