package menubar

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const Version = 1

type Status struct {
	Version  int    `json:"version"`
	State    string `json:"state"`
	Active   int    `json:"active"`
	Category string `json:"category"`
}

type Event struct {
	Version int    `json:"version"`
	Event   string `json:"event"`
}

type Session struct {
	events  chan string
	failed  chan struct{}
	done    chan struct{}
	updates chan Status
	cancel  context.CancelFunc
	close   sync.Once
	mu      sync.Mutex
	err     error
}

func (s Status) Valid() bool {
	if s.Version != Version || s.Active < 0 {
		return false
	}
	switch s.State {
	case "starting", "ready", "serving", "recovering", "error", "draining", "paused", "updating", "stopping":
	default:
		return false
	}
	switch s.Category {
	case "none", "backend", "credentials", "upgrade", "ui":
	default:
		return false
	}
	return true
}

func Start(ctx context.Context, path string, timeout time.Duration) (*Session, error) {
	child, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(child, path)
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		input.Close()
		return nil, fmt.Errorf("menu helper: %w", err)
	}
	s := &Session{events: make(chan string, 8), failed: make(chan struct{}), done: make(chan struct{}), updates: make(chan Status, 1), cancel: cancel}
	ready := make(chan struct{})
	go s.read(output, ready)
	go func() {
		encoder := json.NewEncoder(input)
		for {
			select {
			case <-child.Done():
				input.Close()
				return
			case status := <-s.updates:
				if err := encoder.Encode(status); err != nil {
					s.fail(errors.New("menu helper write failed"))
					cancel()
					input.Close()
					return
				}
			}
		}
	}()
	go func() { err := cmd.Wait(); s.fail(fmt.Errorf("menu helper exited: %w", err)); cancel(); close(s.done) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		return s, nil
	case <-s.failed:
		s.Close()
		return nil, s.Err()
	case <-timer.C:
		s.Close()
		return nil, errors.New("menu helper handshake timeout")
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
}

func (s *Session) read(reader io.Reader, ready chan struct{}) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 4096)
	first := true
	for scanner.Scan() {
		var e Event
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Version != Version {
			s.fail(errors.New("menu helper protocol mismatch"))
			s.cancel()
			return
		}
		if first {
			if e.Event != "ready" {
				s.fail(errors.New("menu helper missing handshake"))
				s.cancel()
				return
			}
			first = false
			close(ready)
			continue
		}
		switch e.Event {
		case "pause", "resume", "quit":
		default:
			s.fail(errors.New("menu helper invalid control"))
			s.cancel()
			return
		}
		select {
		case s.events <- e.Event:
		default:
			s.fail(errors.New("menu helper control overflow"))
			s.cancel()
			return
		}
	}
	s.fail(errors.New("menu helper output closed"))
	s.cancel()
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
		close(s.failed)
	}
}
func (s *Session) Err() error              { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Session) Events() <-chan string   { return s.events }
func (s *Session) Failed() <-chan struct{} { return s.failed }
func (s *Session) Update(status Status) error {
	if !status.Valid() {
		return errors.New("invalid menu status")
	}
	select {
	case <-s.failed:
		return s.Err()
	default:
	}
	select {
	case s.updates <- status:
	default:
		select {
		case <-s.updates:
		default:
		}
		select {
		case s.updates <- status:
		default:
		}
	}
	return nil
}
func (s *Session) Close() { s.close.Do(func() { s.cancel(); <-s.done }) }
