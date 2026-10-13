package ollama

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveEntry struct {
	name, body string
	mode       fs.FileMode
}

func recoveryArchive(t *testing.T, entries []archiveEntry) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "fixture.zip")
	output, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(entry.mode)
		stream, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestRecoveryArchiveRejectsRawParentTraversal(t *testing.T) {
	archive := recoveryArchive(t, []archiveEntry{{"Ollama.app/Contents/../Resources/ollama", "fixture", 0755}})
	if err := ValidateMacArchive(archive); err == nil {
		t.Fatal("raw parent traversal accepted")
	}
}

func TestRecoveryCanceledStartDoesNotLaunch(t *testing.T) {
	down := recoveryOfflineClient(t)
	binary := "/opt/homebrew/bin/ollama"
	system, runner, _ := fakeSystem("darwin", []string{binary}, down)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := system.Start(ctx); err == nil {
		t.Fatal("canceled start succeeded")
	}
	if len(runner.starts)+len(runner.runs) != 0 {
		t.Fatal("canceled start launched a process")
	}
}

type testProcess struct{ running bool }

func (p *testProcess) Running() bool              { return p.running }
func (p *testProcess) Stop(context.Context) error { p.running = false; return nil }

type countedManagedRunner struct {
	fakeRunner
	count int
}

func (r *countedManagedRunner) StartManaged(string, ...string) (Process, error) {
	r.count++
	return &testProcess{running: true}, nil
}

func TestRecoveryDoesNotReplaceLiveOwnedHandle(t *testing.T) {
	down := recoveryOfflineClient(t)
	binary := "/opt/homebrew/bin/ollama"
	system, _, _ := fakeSystem("darwin", []string{binary}, down)
	runner := &countedManagedRunner{}
	system.Runner = runner
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.count != 1 {
		t.Fatalf("started %d owned processes", runner.count)
	}
}

func TestRecoveryArchiveBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		entries []archiveEntry
	}{
		{"duplicate", []archiveEntry{{"Ollama.app/file", "a", 0644}, {"Ollama.app/file", "b", 0644}}},
		{"absolute", []archiveEntry{{"/Applications/Ollama.app/file", "a", 0644}}},
		{"escape", []archiveEntry{{"../escape", "a", 0644}}},
		{"absolute_symlink", []archiveEntry{{"Ollama.app/Contents/Resources/ollama", "/bin/sh", fs.ModeSymlink | 0755}}},
		{"symlink_parent_write", []archiveEntry{{"Ollama.app/Contents/alias", "Resources", fs.ModeSymlink | 0755}, {"Ollama.app/Contents/alias/ollama", "a", 0755}}},
		{"symlink_parent_escape", []archiveEntry{{"Ollama.app/Contents/alias", "../../outside", fs.ModeSymlink | 0755}}},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			if err := ValidateMacArchive(recoveryArchive(t, scenario.entries)); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	safe := recoveryArchive(t, []archiveEntry{{"Ollama.app/Contents/Resources/ollama", "inert", 0755}, {"Ollama.app/Contents/Resources/alias", "ollama", fs.ModeSymlink | 0755}})
	if err := ValidateMacArchive(safe); err != nil {
		t.Fatalf("contained symlink rejected: %v", err)
	}
}

type recoveryTransport func(*http.Request) (*http.Response, error)

func (f recoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestRecoveryInstallerRefusesHTTPSDowngrade(t *testing.T) {
	calls := 0
	transport := recoveryTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://fixture.example.invalid/archive"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	target := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadWithTransport(context.Background(), "https://fixture.example.invalid/archive", target, transport); err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("downgrade error: %v", err)
	}
	if calls != 1 {
		t.Fatal("download followed insecure redirect")
	}
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("insecure download created a file")
	}
}

func TestRecoveryInstallerSizeAndExistingFile(t *testing.T) {
	transport := recoveryTransport(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: MaxInstallerBytes + 1, Body: io.NopCloser(strings.NewReader("tiny fixture")), Request: request}, nil
	})
	target := filepath.Join(t.TempDir(), "archive.zip")
	if err := downloadWithTransport(context.Background(), "https://fixture.example.invalid/archive", target, transport); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	if _, err := os.Stat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("oversized installer retained partial file")
	}
	if err := os.WriteFile(target, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := downloadWithTransport(context.Background(), "https://fixture.example.invalid/archive", target, transport); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("existing download file: %v", err)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "preserved" {
		t.Fatal("existing installer file overwritten")
	}
}

func TestWindowsStartKeepsOwnedHandleAndStopsOnlyOwned(t *testing.T) {
	system, _, _ := fakeSystem("windows", []string{"ollama.exe"}, recoveryOfflineClient(t))
	system.LookPath = func(string) (string, error) { return "ollama.exe", nil }
	runner := &countedManagedRunner{}
	system.Runner = runner
	for range 2 {
		if err := system.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if runner.count != 1 || !system.Owned() {
		t.Fatalf("owned=%v starts=%d", system.Owned(), runner.count)
	}
	if err := system.StopOwned(context.Background()); err != nil {
		t.Fatal(err)
	}
	if system.Owned() {
		t.Fatal("stopped child still owned")
	}
	if err := system.StopOwned(context.Background()); err != nil {
		t.Fatal(err)
	}
}
