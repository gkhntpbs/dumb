//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gkhntpbs/dumb/internal/service"
	"github.com/gkhntpbs/dumb/internal/windowstray"
	"golang.org/x/sys/windows/svc"
)

func platformRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "internal-tray" {
		if windowstray.Run(os.Stdin, os.Stdout) != nil {
			return exitError
		}
		return exitOK
	}
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(stderr, "dumb-worker: service detection:", err)
		return exitError
	}
	if !isService {
		if len(args) == 0 || ((len(args) == 1 || len(args) == 2) && (args[0] == "desktop" || args[0] == "desktop-worker")) {
			root, err := desktopRoot()
			if err != nil {
				return exitError
			}
			if len(args) > 0 && args[0] == "desktop-worker" {
				return desktopWorker(ctx, root)
			}
			if len(args) > 0 {
				exe, err := os.Executable()
				if err != nil {
					return exitError
				}
				if !strings.HasPrefix(filepath.Base(exe), "supervisor-") {
					if err := startDesktop(root, false); err != nil {
						return exitError
					}
					return exitOK
				}
				nonce := ""
				if len(args) == 2 {
					nonce = args[1]
				}
				return desktopSupervisor(ctx, root, nonce)
			}
			if _, err := loadDesktopConfig(root); err == nil {
				if err := startDesktop(root, false); err != nil {
					return exitError
				}
				return exitOK
			} else if !errors.Is(err, os.ErrNotExist) {
				return desktopSetup(ctx, root)
			}
			return desktopSetup(ctx, root)
		}
		return run(ctx, args, stdout, stderr)
	}
	h := &serviceHandler{ctx: ctx, args: args, stdout: stdout, stderr: stderr}
	if err := svc.Run(service.DefaultLabel, h); err != nil {
		fmt.Fprintln(stderr, "dumb-worker: service:", err)
		return exitError
	}
	return h.code
}

type serviceHandler struct {
	ctx            context.Context
	args           []string
	stdout, stderr io.Writer
	code           int
	execute        func(context.Context) int
}

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	status <- svc.Status{State: svc.StartPending}
	finished := make(chan int, 1)
	go func() {
		if h.execute != nil {
			finished <- h.execute(ctx)
		} else {
			finished <- run(ctx, h.args, h.stdout, h.stderr)
		}
	}()
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	status <- current
	var tick <-chan time.Time
	var timer *time.Ticker
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case code := <-finished:
			h.code = code
			return code != 0, uint32(code)
		case request, ok := <-requests:
			if !ok {
				cancel()
				requests = nil
				continue
			}
			switch request.Cmd {
			case svc.Interrogate:
				status <- current
			case svc.Stop, svc.Shutdown:
				cancel()
				current = svc.Status{State: svc.StopPending, WaitHint: 10000, CheckPoint: 1}
				status <- current
				if timer == nil {
					timer = time.NewTicker(2 * time.Second)
					tick = timer.C
				}
			}
		case <-tick:
			current.CheckPoint++
			status <- current
		}
	}
}
