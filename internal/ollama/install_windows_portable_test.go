package ollama

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func portableTestSystem(t *testing.T, archive string) *System {
	t.Helper()
	s := NewSystem(nil)
	s.GOOS = "windows"
	s.WindowsDataDir = filepath.Join(t.TempDir(), "owned")
	content, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	s.WindowsArchive = archive
	s.WindowsArchiveSHA256 = hex.EncodeToString(digest[:])
	return s
}
func TestWindowsPortableInstallPublishesAndPreservesModels(t *testing.T) {
	archive := recoveryArchive(t, []archiveEntry{{"ollama.exe", "MZinert fixture", 0600}, {"lib/ollama/cpu.dll", "inert dependency", 0600}})
	s := portableTestSystem(t, archive)
	models := filepath.Join(s.windowsRoot(), "models", "existing")
	os.MkdirAll(filepath.Dir(models), 0700)
	os.WriteFile(models, []byte("preserve"), 0600)
	if err := s.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ollama.exe", "lib/ollama/cpu.dll", ".dumb-owned.json"} {
		if _, err := os.Stat(filepath.Join(s.windowsRoot(), "Ollama", filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(models); string(data) != "preserve" {
		t.Fatal("model changed")
	}
	if err := s.Install(context.Background()); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("existing target %v", err)
	}
	stages, _ := filepath.Glob(filepath.Join(s.windowsRoot(), ".ollama-install-*"))
	if len(stages) != 0 {
		t.Fatal("staging leak", stages)
	}
}
func TestWindowsPortableArchiveRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "C:/escape", "dir\\escape", "file:stream", "CON.txt", "dir/trailing.", "dir/trailing ", "dir/../escape"} {
		t.Run(name, func(t *testing.T) {
			archive := recoveryArchive(t, []archiveEntry{{"ollama.exe", "MZ", 0600}, {name, "inert", 0600}})
			if err := ValidateWindowsArchive(archive); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, entries := range [][]archiveEntry{
		{{"ollama.exe", "MZ", 0600}, {"OLLAMA.EXE", "MZ", 0600}},
		{{"ollama.exe", "MZ", fs.ModeSymlink | 0600}},
		{{"library.dll", "inert", 0600}},
	} {
		if err := ValidateWindowsArchive(recoveryArchive(t, entries)); err == nil {
			t.Fatal("unsafe/missing CLI accepted")
		}
	}
}
func TestWindowsPortableInstallCancellationAndDigest(t *testing.T) {
	archive := recoveryArchive(t, []archiveEntry{{"ollama.exe", "MZinert", 0600}})
	s := portableTestSystem(t, archive)
	s.WindowsArchiveSHA256 = "00"
	if err := s.Install(context.Background()); err == nil {
		t.Fatal("untrusted local archive accepted")
	}
	if _, err := os.Stat(s.windowsBinary()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("invalid install published")
	}
	s = portableTestSystem(t, archive)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Install(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.windowsRoot()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("canceled install touched disk")
	}
}
func TestWindowsPortableHTTPSInstallerUsesOfficialURL(t *testing.T) {
	archive := recoveryArchive(t, []archiveEntry{{"ollama.exe", "MZinert", 0600}})
	s := portableTestSystem(t, archive)
	s.WindowsArchive = ""
	s.WindowsArchiveSHA256 = ""
	s.Download = func(_ context.Context, url, destination string) error {
		if url != WindowsDownloadURL {
			t.Fatal("unofficial download URL", url)
		}
		source, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer source.Close()
		target, err := os.Create(destination)
		if err != nil {
			return err
		}
		defer target.Close()
		_, err = io.Copy(target, source)
		return err
	}
	if err := s.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type modelsEnvironmentRunner struct {
	fakeRunner
	environment []string
}

func (r *modelsEnvironmentRunner) StartManagedEnvironment(_ string, environment []string, _ ...string) (Process, error) {
	r.environment = append([]string(nil), environment...)
	return &testProcess{running: true}, nil
}

func TestWindowsOwnedModelsSelectionReachesChildWithoutLaunchEnvironment(t *testing.T) {
	t.Setenv("OLLAMA_MODELS", "")
	root := filepath.Join(t.TempDir(), "owned")
	models := filepath.Join(t.TempDir(), "retained-models")
	binary := filepath.Join(root, "Ollama", "ollama.exe")
	system, _, _ := fakeSystem("windows", []string{binary}, recoveryOfflineClient(t))
	system.WindowsDataDir = root
	system.WindowsModelsDir = models
	runner := &modelsEnvironmentRunner{}
	system.Runner = runner
	if err := system.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range runner.environment {
		if value == "OLLAMA_MODELS="+models {
			found = true
		}
	}
	if !found {
		t.Fatal("saved model directory did not reach owned Ollama child")
	}
	closeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := system.StopOwned(closeCtx); err != nil {
		t.Fatal(err)
	}
}
