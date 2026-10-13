package ollama

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

type Process interface {
	Running() bool
	Stop(context.Context) error
}

type EnvironmentProcessRunner interface {
	StartManagedEnvironment(string, []string, ...string) (Process, error)
}

type ProcessRunner interface {
	StartManaged(string, ...string) (Process, error)
}

type ownedProcess struct {
	command *exec.Cmd
	done    chan struct{}
}

func (p *ownedProcess) PID() int { return p.command.Process.Pid }

func (p *ownedProcess) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *ownedProcess) Stop(ctx context.Context) error {
	if !p.Running() {
		return nil
	}
	if err := interruptOwned(p.command.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-p.done:
			return nil
		case <-time.After(time.Second):
			return ctx.Err()
		}
	}
}

func (ExecRunner) StartManaged(name string, args ...string) (Process, error) {
	return (ExecRunner{}).StartManagedEnvironment(name, nil, args...)
}

func (ExecRunner) StartManagedEnvironment(name string, environment []string, args ...string) (Process, error) {
	command := exec.Command(name, args...)
	detach(command)
	command.Env = append(os.Environ(), environment...)
	command.Env = append(command.Env, "OLLAMA_HOST=127.0.0.1:11434")
	command.Stdout, command.Stderr = os.Stderr, os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	cleanup, err := attachOwnedProcess(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	process := &ownedProcess{command: command, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		cleanup()
		close(process.done)
	}()
	return process, nil
}

func (s *System) Owned() bool {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	return s.process != nil && s.process.Running()
}
func (s *System) StopOwned(ctx context.Context) error {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	if s.process == nil {
		return nil
	}
	err := s.process.Stop(ctx)
	if err == nil {
		s.process = nil
	}
	return err
}

// OwnedPID is a scoped diagnostic handle for the worker's own child only.
// Unknown or already exited processes are never returned for recovery actions.
func (s *System) OwnedPID() int {
	s.processMu.Lock()
	defer s.processMu.Unlock()
	if s.process == nil || !s.process.Running() {
		return 0
	}
	if process, ok := s.process.(interface{ PID() int }); ok {
		return process.PID()
	}
	return 0
}
