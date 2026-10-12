package recoverylab

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
)

type Snapshot struct {
	Downloads, Extracts, Starts, Stops, Denied, Pulls int
	DiskBytes                                         int64
	PeakFixtureBytes                                  int64
}
type Fixture struct {
	t         testing.TB
	rootPath  string
	done      chan struct{}
	root      *os.Root
	home      string
	endpoint  *httptest.Server
	client    *ollama.Client
	system    *ollama.System
	mu        sync.Mutex
	running   bool
	models    map[string]bool
	fault     string
	remaining int
	counts    Snapshot
}

func New(t testing.TB) *Fixture {
	t.Helper()
	rootPath := filepath.Clean(t.TempDir())
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &Fixture{t: t, rootPath: rootPath, root: root, home: filepath.Join(rootPath, "home"), models: map[string]bool{}, done: make(chan struct{})}
	if err := root.MkdirAll("home", 0700); err != nil {
		t.Fatal(err)
	}
	fixture.endpoint = httptest.NewServer(http.HandlerFunc(fixture.serve))
	t.Cleanup(func() {
		close(fixture.done)
		fixture.endpoint.CloseClientConnections()
		fixture.endpoint.Close()
		root.Close()
	})
	fixture.client, err = ollama.NewWithTransport(ollama.DefaultURL, fixture.transport(fixture.endpoint.URL, true))
	if err != nil {
		t.Fatal(err)
	}
	guarded := &scopedFS{fixture: fixture}
	fixture.system = &ollama.System{
		GOOS: "darwin", Home: fixture.home, Client: fixture.client, FS: guarded,
		LookPath: func(string) (string, error) { return "", fs.ErrNotExist },
		Stat: func(p string) (fs.FileInfo, error) {
			if !strings.HasPrefix(filepath.Clean(p), fixture.rootPath+string(filepath.Separator)) {
				return nil, fs.ErrNotExist
			}
			return guarded.Stat(p)
		},
		Writable: func(string) bool { return false },
		TempDir:  func() (string, error) { return "", errors.New("lab: unscoped temp directory rejected") },
		Runner:   &simulatedRunner{fixture}, Download: fixture.download,
		ValidateArchive: func(p string) error {
			if _, err := guarded.Stat(p); err != nil {
				return err
			}
			return ollama.ValidateMacArchive(p)
		},
	}
	return fixture
}

func (f *Fixture) Client() *ollama.Client { return f.client }
func (f *Fixture) System() *ollama.System { return f.system }
func (f *Fixture) Recovery(external bool) *ollama.Recovery {
	recovery, err := ollama.NewRecovery(ollama.RecoveryConfig{System: f.system, Models: []string{"fixture"}, External: external, Attempts: 3, ProbeTimeout: 15 * time.Millisecond, StartTimeout: 40 * time.Millisecond, InstallTimeout: 100 * time.Millisecond, PullTimeout: 40 * time.Millisecond, Backoff: time.Millisecond, Cooldown: 50 * time.Millisecond, MaxCooldown: 100 * time.Millisecond, PollInterval: time.Millisecond})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := recovery.Close(ctx); err != nil {
			f.t.Error(err)
		}
	})
	return recovery
}
func (f *Fixture) SetRunning(v bool)    { f.mu.Lock(); f.running = v; f.mu.Unlock() }
func (f *Fixture) AddModel(name string) { f.mu.Lock(); f.models[name] = true; f.mu.Unlock() }
func (f *Fixture) Fault(kind string, count int) {
	f.mu.Lock()
	f.fault = kind
	f.remaining = count
	f.mu.Unlock()
}
func (f *Fixture) consume(kind string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fault != kind || f.remaining == 0 {
		return false
	}
	if f.remaining > 0 {
		f.remaining--
		if f.remaining == 0 {
			f.fault = ""
		}
	}
	return true
}
func (f *Fixture) Snapshot() Snapshot {
	f.mu.Lock()
	snapshot := f.counts
	f.mu.Unlock()
	fs.WalkDir(f.root.FS(), ".", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			snapshot.DiskBytes += info.Size()
		}
		return nil
	})
	if snapshot.DiskBytes > snapshot.PeakFixtureBytes {
		snapshot.PeakFixtureBytes = snapshot.DiskBytes
	}
	f.mu.Lock()
	if snapshot.PeakFixtureBytes > f.counts.PeakFixtureBytes {
		f.counts.PeakFixtureBytes = snapshot.PeakFixtureBytes
	}
	f.mu.Unlock()
	return snapshot
}
func (f *Fixture) InstallPresent() {
	binary := filepath.Join(f.home, "Applications", "Ollama.app", "Contents", "Resources", "ollama")
	guarded := &scopedFS{fixture: f}
	if err := guarded.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		f.t.Fatal(err)
	}
	relative, err := guarded.relative(binary)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.root.WriteFile(relative, []byte("non-executable fixture content"), 0755); err != nil {
		f.t.Fatal(err)
	}
}
func (f *Fixture) deny() error {
	f.mu.Lock()
	f.counts.Denied++
	f.mu.Unlock()
	return errors.New("lab: operation escaped registered fixture boundary")
}

func (f *Fixture) transport(endpoint string, alias bool) http.RoundTripper {
	base, err := url.Parse(endpoint)
	if err != nil {
		f.t.Fatal(err)
	}
	if base.Hostname() != "127.0.0.1" || base.Port() == "11434" || base.Port() == "" {
		f.t.Fatal("lab: fixture must use a random IPv4 loopback port")
	}
	native := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != base.Host {
			return nil, f.deny()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	f.t.Cleanup(native.CloseIdleConnections)
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "http" || (request.URL.Host != base.Host && !(alias && request.URL.Host == "127.0.0.1:11434")) {
			return nil, f.deny()
		}
		if alias {
			f.mu.Lock()
			running := f.running
			f.mu.Unlock()
			if !running {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
			}
			if request.URL.Path != "/api/version" && request.URL.Path != "/api/tags" && request.URL.Path != "/api/pull" && request.URL.Path != "/api/generate" {
				return nil, f.deny()
			}
		}
		cloned := request.Clone(request.Context())
		target := *request.URL
		target.Host = base.Host
		cloned.URL = &target
		cloned.Host = base.Host
		return native.RoundTrip(cloned)
	})
}
func (f *Fixture) TransportFor(endpoint string) http.RoundTripper {
	return f.transport(endpoint, false)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func (f *Fixture) serve(w http.ResponseWriter, r *http.Request) {
	if _, err := io.Copy(io.Discard, io.LimitReader(r.Body, 16<<10)); err != nil {
		return
	}
	if f.consume("api_hang") || (r.URL.Path == "/api/tags" && f.consume("tags_hang")) {
		select {
		case <-r.Context().Done():
		case <-f.done:
		}
		return
	}
	if f.consume("api_malformed") || (r.URL.Path == "/api/tags" && f.consume("tags_malformed")) {
		fmt.Fprint(w, "{")
		return
	}
	if f.consume("api_unavailable") || (r.URL.Path == "/api/tags" && f.consume("tags_unavailable")) || (r.URL.Path == "/api/generate" && f.consume("generate_unavailable")) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"fixture unavailable"}`)
		return
	}
	switch r.URL.Path {
	case "/api/version":
		fmt.Fprint(w, `{"version":"fixture"}`)
	case "/api/tags":
		f.mu.Lock()
		names := make([]string, 0, len(f.models))
		for name := range f.models {
			names = append(names, name)
		}
		f.mu.Unlock()
		fmt.Fprint(w, `{"models":[`)
		for i, name := range names {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"name":%q}`, name)
		}
		fmt.Fprint(w, "]}")
	case "/api/pull":
		f.mu.Lock()
		f.counts.Pulls++
		f.mu.Unlock()
		if f.consume("pull_interrupted") {
			fmt.Fprintln(w, `{"status":"pulling manifest"}`)
			return
		}
		if f.consume("pull_hang") {
			select {
			case <-r.Context().Done():
			case <-f.done:
			}
			return
		}
		if f.consume("pull_missing") {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":"fixture model absent"}`)
			return
		}
		f.AddModel("fixture")
		fmt.Fprintln(w, `{"status":"success"}`)
	case "/api/generate":
		fmt.Fprint(w, `{"response":"fixture answer","done":true}`)
	default:
		w.WriteHeader(404)
	}
}

func (f *Fixture) download(ctx context.Context, source, destination string) error {
	defer f.Snapshot()
	if source != ollama.MacOSDownloadURL {
		return f.deny()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.counts.Downloads++
	f.mu.Unlock()
	guarded := &scopedFS{fixture: f}
	relative, err := guarded.relative(destination)
	if err != nil {
		return err
	}
	if f.consume("download_interrupted") {
		f.root.WriteFile(relative, []byte("partial"), 0600)
		return io.ErrUnexpectedEOF
	}
	if f.consume("download_corrupt") {
		return f.root.WriteFile(relative, []byte("not a zip"), 0600)
	}
	output, err := f.root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	header := &zip.FileHeader{Name: "Ollama.app/Contents/Resources/ollama", Method: zip.Store}
	header.SetMode(0755)
	stream, err := writer.CreateHeader(header)
	if err == nil {
		_, err = stream.Write([]byte("tiny inert CLI fixture"))
	}
	if err != nil {
		writer.Close()
		output.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

type scopedFS struct{ fixture *Fixture }

func (s *scopedFS) relative(p string) (string, error) {
	relative, err := filepath.Rel(s.fixture.rootPath, filepath.Clean(p))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", s.fixture.deny()
	}
	return relative, nil
}
func (s *scopedFS) Lstat(p string) (fs.FileInfo, error) {
	relative, err := s.relative(p)
	if err != nil {
		return nil, err
	}
	return s.fixture.root.Lstat(relative)
}
func (s *scopedFS) Stat(p string) (fs.FileInfo, error) {
	relative, err := s.relative(p)
	if err != nil {
		return nil, err
	}
	return s.fixture.root.Stat(relative)
}
func (s *scopedFS) MkdirAll(p string, mode fs.FileMode) error {
	relative, err := s.relative(p)
	if err != nil {
		return err
	}
	if s.fixture.consume("permission") {
		return fs.ErrPermission
	}
	if s.fixture.consume("disk_full") {
		return syscall.ENOSPC
	}
	return s.fixture.root.MkdirAll(relative, mode)
}
func (s *scopedFS) MkdirTemp(parent, prefix string) (string, error) {
	relative, err := s.relative(parent)
	if err != nil {
		return "", err
	}
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("%s%d", prefix, i)
		candidate := filepath.Join(relative, name)
		if err := s.fixture.root.Mkdir(candidate, 0700); err == nil {
			return filepath.Join(s.fixture.rootPath, candidate), nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fs.ErrExist
}
func (s *scopedFS) RemoveAll(p string) error {
	relative, err := s.relative(p)
	if err != nil {
		return err
	}
	if relative == "." {
		return s.fixture.deny()
	}
	return s.fixture.root.RemoveAll(relative)
}
func (s *scopedFS) Remove(p string) error {
	relative, err := s.relative(p)
	if err != nil {
		return err
	}
	return s.fixture.root.Remove(relative)
}
func (s *scopedFS) Symlink(target, p string) error {
	defer s.fixture.Snapshot()
	relative, err := s.relative(p)
	if err != nil {
		return err
	}
	if filepath.IsAbs(target) {
		return s.fixture.deny()
	}
	if s.fixture.consume("publish_race") {
		if err := s.fixture.root.Mkdir(relative, 0700); err != nil {
			return err
		}
	}
	return s.fixture.root.Symlink(target, relative)
}

type simulatedRunner struct{ fixture *Fixture }

func (r *simulatedRunner) Run(ctx context.Context, name string, args ...string) error {
	if name != "ditto" || len(args) != 3 || args[0] != "-xk" {
		return r.fixture.deny()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f := r.fixture
	defer f.Snapshot()
	guarded := &scopedFS{fixture: f}
	archive, err := guarded.relative(args[1])
	if err != nil {
		return err
	}
	stage, err := guarded.relative(args[2])
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.counts.Extracts++
	f.mu.Unlock()
	if f.consume("extract_failure") {
		return errors.New("lab: extraction failed")
	}
	file, err := f.root.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return err
	}
	for _, entry := range reader.File {
		target := filepath.Join(stage, filepath.FromSlash(entry.Name))
		if entry.UncompressedSize64 > 16<<10 {
			return errors.New("lab: oversized fixture rejected")
		}
		if err := f.root.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(source, 16<<10))
		source.Close()
		if err != nil {
			return err
		}
		if err := f.root.WriteFile(target, data, entry.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
func (r *simulatedRunner) Start(name string, args ...string) error { return r.fixture.deny() }
func (r *simulatedRunner) StartManaged(name string, args ...string) (ollama.Process, error) {
	f := r.fixture
	expected := filepath.Join(f.home, "Applications", "Ollama.app", "Contents", "Resources", "ollama")
	if name != expected || len(args) != 1 || args[0] != "serve" {
		return nil, f.deny()
	}
	f.mu.Lock()
	f.counts.Starts++
	f.mu.Unlock()
	if f.consume("start_failure") {
		return nil, errors.New("lab: start failed")
	}
	crashed := f.consume("start_crash")
	f.SetRunning(!crashed)
	return &simulatedProcess{fixture: f, running: !crashed}, nil
}

type simulatedProcess struct {
	fixture *Fixture
	mu      sync.Mutex
	running bool
}

func (p *simulatedProcess) Running() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.running }
func (p *simulatedProcess) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
	p.fixture.mu.Lock()
	p.fixture.counts.Stops++
	p.fixture.running = false
	p.fixture.mu.Unlock()
	return nil
}
