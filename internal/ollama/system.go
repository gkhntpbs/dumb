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
	"sync"
	"time"
)

// Official installers. All are fetched over HTTPS from ollama.com.
const (
	LinuxInstallScript = "curl -fsSL https://ollama.com/install.sh | sh"
	MacOSDownloadURL   = "https://ollama.com/download/Ollama-darwin.zip"
	WindowsDownloadURL = "https://github.com/ollama/ollama/releases/latest/download/ollama-windows-amd64.zip"
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
	hideConsole(cmd)
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
	GOOS                 string
	Home                 string
	LocalAppData         string // Windows %LOCALAPPDATA%
	WindowsDataDir       string // optional owned portable install/model root
	WindowsModelsDir     string
	WindowsArchive       string // optional securely relayed official archive
	WindowsArchiveSHA256 string // required SHA256 for a local archive
	LookPath             func(string) (string, error)
	Stat                 func(string) (fs.FileInfo, error)
	Writable             func(dir string) bool
	Download             func(ctx context.Context, url, dst string) error
	TempDir              func() (string, error)
	Runner               Runner
	Client               *Client
	FS                   InstallFileSystem
	ValidateArchive      func(string) error
	processMu            sync.Mutex
	process              Process
}

// NewSystem returns a System wired to the real machine.
func NewSystem(c *Client) *System {
	home, _ := os.UserHomeDir()
	return &System{
		GOOS:                 runtime.GOOS,
		Home:                 home,
		LocalAppData:         os.Getenv("LOCALAPPDATA"),
		WindowsDataDir:       os.Getenv("DUMB_OLLAMA_DATA_DIR"),
		WindowsArchive:       os.Getenv("DUMB_OLLAMA_INSTALL_ARCHIVE"),
		WindowsArchiveSHA256: os.Getenv("DUMB_OLLAMA_INSTALL_SHA256"),
		LookPath:             exec.LookPath,
		Stat:                 os.Stat,
		Writable:             dirWritable,
		Download:             download,
		TempDir:              func() (string, error) { return os.MkdirTemp("", "dumb-ollama-") },
		Runner:               ExecRunner{},
		Client:               c,
		FS:                   OSInstallFileSystem{},
		ValidateArchive:      ValidateMacArchive,
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
		return []string{filepath.Join(s.LocalAppData, "Programs", "Ollama", "ollama.exe"), s.windowsBinary()}
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
	if s.Client != nil && s.Client.remote {
		return ErrRemoteManaged
	}
	switch s.GOOS {
	case "linux":
		return s.Runner.Run(ctx, "sh", "-c", LinuxInstallScript)
	case "darwin":
		return s.installMac(ctx)
	case "windows":
		return s.installWindows(ctx)
	default:
		return fmt.Errorf("ollama: automatic install is not supported on %s; see https://ollama.com/download", s.GOOS)
	}
}

// Start starts an installed Ollama that is not running.
func (s *System) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Client != nil && s.Client.remote {
		return ErrRemoteManaged
	}
	d := s.Detect(ctx)
	switch {
	case d.Reachable:
		return nil
	case !d.Installed:
		return errors.New("ollama: not installed")
	}
	switch s.GOOS {
	case "darwin", "windows":
		s.processMu.Lock()
		defer s.processMu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.process != nil && s.process.Running() {
			return nil
		}
		binary := d.Path
		if filepath.Ext(binary) == ".app" {
			binary = filepath.Join(binary, "Contents", "Resources", "ollama")
		}
		if _, err := s.Stat(binary); err != nil {
			return err
		}
		ownedWindows := filepath.Clean(binary) == filepath.Clean(s.windowsBinary())
		if s.GOOS == "windows" && (ownedWindows || s.WindowsModelsDir != "") {
			if runner, ok := s.Runner.(EnvironmentProcessRunner); ok {
				var extra []string
				if ownedWindows {
					extra = append(extra, "USERPROFILE="+filepath.Join(s.windowsRoot(), "home"))
				}
				if os.Getenv("OLLAMA_MODELS") == "" {
					models := s.WindowsModelsDir
					if models == "" && ownedWindows {
						models = filepath.Join(s.windowsRoot(), "models")
					}
					if models != "" {
						extra = append(extra, "OLLAMA_MODELS="+models)
					}
				}
				process, err := runner.StartManagedEnvironment(binary, extra, "serve")
				if err != nil {
					return err
				}
				s.process = process
				return nil
			}
		}
		if runner, ok := s.Runner.(ProcessRunner); ok {
			process, err := runner.StartManaged(binary, "serve")
			if err != nil {
				return err
			}
			s.process = process
			return nil
		}
		return s.Runner.Start(binary, "serve")
	case "linux":
		if err := s.Runner.Run(ctx, "systemctl", "start", "ollama"); err == nil {
			return nil
		}
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
	return downloadWithTransport(ctx, url, dst, http.DefaultTransport)
}

func downloadWithTransport(ctx context.Context, url, dst string, transport http.RoundTripper) error {
	if transport == nil {
		return errors.New("ollama: installer HTTP transport is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("ollama: refusing non-HTTPS download %s", url)
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("ollama: installer redirect must use HTTPS")
		}
		if len(via) >= 10 {
			return errors.New("ollama: too many installer redirects")
		}
		return nil
	}}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: download %s: %s", url, res.Status)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if res.ContentLength > MaxInstallerBytes {
		f.Close()
		os.Remove(dst)
		return errors.New("ollama: installer exceeds download limit")
	}
	if size, err := io.Copy(f, io.LimitReader(res.Body, MaxInstallerBytes+1)); err != nil || size > MaxInstallerBytes {
		f.Close()
		os.Remove(dst)
		if err != nil {
			return err
		}
		return errors.New("ollama: installer exceeds download limit")
	}
	return f.Close()
}
