package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gkhntpbs/dumb/internal/menubar"
	"github.com/gkhntpbs/dumb/internal/worker"
)

type menuView struct {
	mu             sync.Mutex
	session        *menubar.Session
	snapshot       worker.Status
	mode, category string
	updating       bool
}

func (v *menuView) worker(status worker.Status) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.snapshot = status
	v.render()
}

func (v *menuView) lifecycle(mode, category string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if mode == "updating" {
		v.updating = true
	} else if mode == "" {
		v.updating = false
	} else {
		v.mode, v.category = mode, category
	}
	v.render()
}

func (v *menuView) render() {
	v.session.Update(v.status())
}

func (v *menuView) status() menubar.Status {
	state, category := v.mode, v.category
	if v.updating && state != "draining" && state != "paused" && state != "stopping" && state != "error" {
		state = "updating"
	}
	if state == "" || state == "starting" {
		state, category = "recovering", "backend"
		if v.snapshot.Error {
			state = "error"
		}
		if v.snapshot.Ready {
			state, category = "ready", "none"
		}
		if v.snapshot.Active > 0 {
			state = "serving"
		}
	}
	return menubar.Status{Version: menubar.Version, State: state, Active: v.snapshot.Active, Category: category}
}

func menuHelper(exe string) (string, error) {
	path := filepath.Join(filepath.Dir(exe), "..", "Resources", "DUMBMenu.app", "Contents", "MacOS", "dumb-menu")
	if filepath.Base(filepath.Dir(exe)) != "MacOS" {
		return "", fmt.Errorf("macOS run requires the application bundle or explicit -headless")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("menu helper missing: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("menu helper is not executable")
	}
	return path, nil
}
