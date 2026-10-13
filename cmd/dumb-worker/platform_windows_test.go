//go:build windows

package main

import (
	"context"
	"golang.org/x/sys/windows/svc"
	"testing"
	"time"
)

func TestWindowsServiceStopCancelsOnlyOwnedWorker(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 2)
	statuses := make(chan svc.Status, 16)
	done := make(chan uint32, 1)
	h := &serviceHandler{ctx: context.Background(), execute: func(ctx context.Context) int { <-ctx.Done(); return exitOK }}
	go func() { _, code := h.Execute(nil, requests, statuses); done <- code }()
	if s := <-statuses; s.State != svc.StartPending {
		t.Fatal(s)
	}
	running := <-statuses
	if running.State != svc.Running || running.Accepts&svc.AcceptStop == 0 {
		t.Fatal(running)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Interrogate}
	if s := <-statuses; s.State != svc.Running {
		t.Fatal(s)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	if s := <-statuses; s.State != svc.StopPending {
		t.Fatal(s)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}
func TestWindowsServiceRestartExitIsServiceSpecific(t *testing.T) {
	h := &serviceHandler{ctx: context.Background(), execute: func(context.Context) int { return exitRestart }}
	statuses := make(chan svc.Status, 8)
	specific, code := h.Execute(nil, make(chan svc.ChangeRequest), statuses)
	if !specific || code != 4 || h.code != 4 {
		t.Fatalf("specific=%v code=%d", specific, code)
	}
}
