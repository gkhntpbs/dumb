// tray-preview is an inert desktop acceptance fixture. It never runs a worker,
// opens network sockets or installs anything.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/gkhntpbs/dumb/internal/menubar"
	"io"
	"os"
	"time"
)

func main() {
	helper := flag.String("helper", "", "native tray helper path")
	duration := flag.Duration("duration", 2*time.Minute, "maximum preview lifetime")
	logPath := flag.String("log", "", "write sanitized preview evidence to this file")
	flag.Parse()
	var output io.Writer = os.Stdout
	if *logPath != "" {
		file, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			os.Exit(1)
		}
		defer file.Close()
		output = file
	}
	fmt.Fprintln(output, "preview started; inert fixture, no worker or sockets")
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	session, err := menubar.Start(ctx, *helper, 5*time.Second)
	if err != nil {
		fmt.Fprintln(output, err)
		os.Exit(1)
	}
	defer session.Close()
	states := []string{"starting", "ready", "serving", "recovering", "error", "updating"}
	current := menubar.Status{Version: 1, State: "starting", Category: "none"}
	next := time.NewTicker(8 * time.Second)
	defer next.Stop()
	index := 0
	paused := false
	send := func() {
		_ = session.Update(current)
		fmt.Fprintf(output, "status %s active=%d category=%s\n", current.State, current.Active, current.Category)
	}
	send()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(output, "preview finished: deadline")
			return
		case <-session.Failed():
			fmt.Fprintln(output, session.Err())
			os.Exit(1)
		case event := <-session.Events():
			fmt.Fprintln(output, "control", event)
			switch event {
			case "pause":
				paused = true
				current.State = "paused"
				current.Active = 0
				current.Category = "none"
			case "resume":
				paused = false
				current.State = "ready"
			case "quit":
				return
			}
			send()
		case <-next.C:
			if paused {
				continue
			}
			index = (index + 1) % len(states)
			current.State = states[index]
			current.Active = 0
			current.Category = "none"
			if current.State == "serving" {
				current.Active = 2
			}
			if current.State == "error" || current.State == "recovering" {
				current.Category = "backend"
			}
			send()
		}
	}
}
