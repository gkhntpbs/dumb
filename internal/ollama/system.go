package ollama

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Official installers. All are fetched over HTTPS from ollama.com.
const (
	LinuxInstallScript = "curl -fsSL https://ollama.com/install.sh | sh"
	MacOSDownloadURL   = "https://ollama.com/download/Ollama-darwin.zip"
	WindowsDownloadURL = "https://ollama.com/download/OllamaSetup.exe"
)

// Runner executes commands. Tests replace it so nothing touches the real system.
type Runner interface {
	// Run runs a command to completion.
	Run(ctx context.Context, name string, args ...string) error
	// Start starts a long-lived command detached from this process.
	Start(name string, args ...string) error
}

// ExecRunner is the real Runner.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func (ExecRunner) Start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return cmd.Process.Release()
}

// System holds every seam Detect, Install and Start use to touch the machine.
type System struct {
	GOOS         string
	Home         string
	LocalAppData string // Windows %LOCALAPPDATA%
	LookPath     func(string) (string, error)
	Stat         func(string) (fs.FileInfo, error)
	Writable     func(dir string) bool
	Download     func(ctx context.Context, url, dst string) error
	TempDir      func() (string, error)
	Runner       Runner
	Client       *Client
}

// NewSystem returns a System wired to the real machine.
func NewSystem(c *Client) *System {
	home, _ := os.UserHomeDir()
	return &System{
		GOOS:         runtime.GOOS,
		Home:         home,
		LocalAppData: os.Getenv("LOCALAPPDATA"),
		LookPath:     exec.LookPath,
		Stat:         os.Stat,
		Writable:     dirWritable,
		Download:     download,
		TempDir:      func() (string, error) { return os.MkdirTemp("", "dumb-ollama-") },
		Runner:       ExecRunner{},
		Client:       c,
	}
}

// Detection is what Detect found.
type Detection struct {
	Path      string // binary or app bundle; "" if none was found
	Reachable bool   // the API answered on loopback
	Version   string
	Installed bool // Path != "" || Reachable
}

// KnownPaths lists where Ollama is installed on s.GOOS, besides $PATH.
func (s *System) KnownPaths() []string {
	switch s.GOOS {
	case "darwin":
		return []string{
			"/Applications/Ollama.app",
			filepath.Join(s.Home, "Applications", "Ollama.app"),
			"/usr/local/bin/ollama",
			"/opt/homebrew/bin/ollama",
		}
	case "windows":
		if s.LocalAppData == "" {
			return nil
		}
		return []string{filepath.Join(s.LocalAppData, "Programs", "Ollama", "ollama.exe")}
	default:
		return []string{"/usr/local/bin/ollama", "/usr/bin/ollama"}
	}
}

// Detect reports whether Ollama is installed and whether its API is up.
func (s *System) Detect(ctx context.Context) Detection {
	var d Detection
	if p, err := s.LookPath("ollama"); err == nil {
		d.Path = p
	} else {
		for _, p := range s.KnownPaths() {
			if _, err := s.Stat(p); err == nil {
				d.Path = p
				break
			}
		}
	}
	if s.Client != nil {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if v, err := s.Client.Version(ctx); err == nil {
			d.Reachable, d.Version = true, v
		}
	}
	d.Installed = d.Path != "" || d.Reachable
	return d
}

// Install installs Ollama the official way for s.GOOS.
func (s *System) Install(ctx context.Context) error {
	switch s.GOOS {
	case "linux":
		return s.Runner.Run(ctx, "sh", "-c", LinuxInstallScript)
	case "darwin":
		tmp, err := s.TempDir()
		if err != nil {
			return err
		}
		zip := filepath.Join(tmp, "Ollama-darwin.zip")
		if err := s.Download(ctx, MacOSDownloadURL, zip); err != nil {
			return err
		}
		dest := "/Applications"
		if !s.Writable(dest) {
			dest = filepath.Join(s.Home, "Applications")
		}
		if err := s.Runner.Run(ctx, "ditto", "-xk", zip, dest); err != nil {
			return err
		}
		return s.Runner.Run(ctx, "open", "-a", filepath.Join(dest, "Ollama.app"))
	case "windows":
		tmp, err := s.TempDir()
		if err != nil {
			return err
		}
		exe := filepath.Join(tmp, "OllamaSetup.exe")
		if err := s.Download(ctx, WindowsDownloadURL, exe); err != nil {
			return err
		}
		return s.Runner.Run(ctx, exe, "/VERYSILENT")
	default:
		return fmt.Errorf("ollama: automatic install is not supported on %s; see https://ollama.com/download", s.GOOS)
	}
}

// Start starts an installed Ollama that is not running.
func (s *System) Start(ctx context.Context) error {
	d := s.Detect(ctx)
	switch {
	case d.Reachable:
		return nil
	case !d.Installed:
		return errors.New("ollama: not installed")
	}
	switch s.GOOS {
	case "darwin":
		return s.Runner.Run(ctx, "open", "-a", "Ollama")
	case "linux":
		if err := s.Runner.Run(ctx, "systemctl", "start", "ollama"); err == nil {
			return nil
		}
		return s.Runner.Start(d.Path, "serve")
	case "windows":
		return s.Runner.Start(d.Path, "serve")
	default:
		return s.Runner.Start(d.Path, "serve")
	}
}

// WaitReachable polls the API until it answers or ctx ends.
func (s *System) WaitReachable(ctx context.Context, every time.Duration) (string, error) {
	for {
		if d := s.Detect(ctx); d.Reachable {
			return d.Version, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("ollama: API not reachable: %w", ctx.Err())
		case <-time.After(every):
		}
	}
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".dumb-write-test-")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("ollama: refusing non-HTTPS download %s", url)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: download %s: %s", url, res.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, res.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
