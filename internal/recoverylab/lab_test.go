package recoverylab

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
)

func stages(f *Fixture) int {
	entries, err := fs.ReadDir(f.root.FS(), "home/Applications")
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".dumb-ollama-install-") {
			count++
		}
	}
	return count
}

func TestRecoveryLabMatrix(t *testing.T) {
	scenarios := []struct {
		name              string
		prepare           func(*Fixture)
		external          bool
		wantError         bool
		downloads, starts int
	}{
		{"present_reachable", func(f *Fixture) { f.InstallPresent(); f.SetRunning(true); f.AddModel("fixture") }, false, false, 0, 0},
		{"installed_stopped", func(f *Fixture) { f.InstallPresent() }, false, false, 0, 1},
		{"absent_install", func(f *Fixture) {}, false, false, 1, 1},
		{"missing_model_proxy", func(f *Fixture) { f.SetRunning(true) }, true, false, 0, 0},
		{"explicit_default_unavailable", func(f *Fixture) { f.InstallPresent() }, true, true, 0, 0},
		{"interrupted_download_recovers", func(f *Fixture) { f.Fault("download_interrupted", 1) }, false, false, 2, 1},
		{"corrupt_download_bounded", func(f *Fixture) { f.Fault("download_corrupt", -1) }, false, true, 3, 0},
		{"extract_failure_bounded", func(f *Fixture) { f.Fault("extract_failure", -1) }, false, true, 3, 0},
		{"permission_permanent", func(f *Fixture) { f.Fault("permission", -1) }, false, true, 0, 0},
		{"disk_full_permanent", func(f *Fixture) { f.Fault("disk_full", -1) }, false, true, 0, 0},
		{"publication_race_preserved", func(f *Fixture) { f.Fault("publish_race", 1) }, false, true, 1, 0},
		{"start_failure_bounded", func(f *Fixture) { f.InstallPresent(); f.Fault("start_failure", -1) }, false, true, 0, 3},
		{"start_crash_bounded", func(f *Fixture) { f.InstallPresent(); f.Fault("start_crash", -1) }, false, true, 0, 3},
		{"occupied_hanging_api", func(f *Fixture) { f.SetRunning(true); f.Fault("api_hang", -1) }, false, true, 0, 0},
		{"occupied_malformed_api", func(f *Fixture) { f.SetRunning(true); f.Fault("api_malformed", -1) }, false, true, 0, 0},
		{"occupied_unavailable_api", func(f *Fixture) { f.SetRunning(true); f.Fault("api_unavailable", -1) }, false, true, 0, 0},
		{"interrupted_pull_recovers", func(f *Fixture) { f.SetRunning(true); f.Fault("pull_interrupted", 1) }, true, false, 0, 0},
		{"hanging_pull_bounded", func(f *Fixture) { f.SetRunning(true); f.Fault("pull_hang", -1) }, true, true, 0, 0},
		{"absent_model_permanent", func(f *Fixture) { f.SetRunning(true); f.Fault("pull_missing", -1) }, true, true, 0, 0},
		{"proxy_tags_malformed", func(f *Fixture) { f.SetRunning(true); f.Fault("tags_malformed", -1) }, true, true, 0, 0},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := New(t)
			scenario.prepare(fixture)
			recovery := fixture.Recovery(scenario.external)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := recovery.Ensure(ctx)
			if (err != nil) != scenario.wantError {
				t.Fatalf("Ensure error=%v, expected error=%v", err, scenario.wantError)
			}
			snapshot := fixture.Snapshot()
			if snapshot.Downloads != scenario.downloads || snapshot.Starts != scenario.starts {
				t.Fatalf("unexpected operation counts: %+v", snapshot)
			}
			if snapshot.Denied != 0 || snapshot.DiskBytes > 64<<10 {
				t.Fatalf("fixture boundary/resource violation: %+v", snapshot)
			}
			if scenario.wantError {
				if err := recovery.Ensure(ctx); !errors.Is(err, ollama.ErrRecoveryBackoff) {
					t.Fatalf("failed cycle did not cool down: %v", err)
				}
				if next := fixture.Snapshot(); next.Downloads != snapshot.Downloads || next.Starts != snapshot.Starts {
					t.Fatal("cooldown repeated an operation")
				}
				if stages(fixture) != 0 {
					t.Fatal("failed install retained staging")
				}
			} else {
				names, err := fixture.Client().List(ctx)
				if err != nil || !ollama.HasModel(names, "fixture") {
					t.Fatalf("not ready after recovery: %v %v", names, err)
				}
			}
			t.Logf("fixture bytes=%d downloads=%d extracts=%d simulated starts=%d stops=%d", snapshot.DiskBytes, snapshot.Downloads, snapshot.Extracts, snapshot.Starts, snapshot.Stops)
		})
	}
}

func TestRecoveryLabOwnedAPIFaults(t *testing.T) {
	for _, fault := range []string{"api_hang", "api_malformed", "api_unavailable", "tags_hang", "tags_malformed", "tags_unavailable"} {
		t.Run(fault, func(t *testing.T) {
			fixture := New(t)
			fixture.InstallPresent()
			recovery := fixture.Recovery(false)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := recovery.Ensure(ctx); err != nil {
				t.Fatal(err)
			}
			fixture.Fault(fault, 1)
			if err := recovery.Ensure(ctx); err != nil {
				t.Fatal(err)
			}
			snapshot := fixture.Snapshot()
			if snapshot.Starts != 2 || snapshot.Stops != 1 || snapshot.Downloads != 0 {
				t.Fatalf("owned fault not repaired safely: %+v", snapshot)
			}
		})
	}
}

func TestRecoveryLabUnmanagedEndpointLoss(t *testing.T) {
	fixture := New(t)
	fixture.SetRunning(true)
	fixture.AddModel("fixture")
	recovery := fixture.Recovery(false)
	if err := recovery.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.SetRunning(false)
	if err := recovery.Ensure(context.Background()); !errors.Is(err, ollama.ErrExternalEndpoint) {
		t.Fatalf("unmanaged loss: %v", err)
	}
	snapshot := fixture.Snapshot()
	if snapshot.Starts+snapshot.Stops+snapshot.Downloads != 0 {
		t.Fatalf("unmanaged endpoint modified: %+v", snapshot)
	}
}

func TestRecoveryLabCancellation(t *testing.T) {
	fixture := New(t)
	fixture.Fault("download_corrupt", -1)
	recovery, err := ollama.NewRecovery(ollama.RecoveryConfig{System: fixture.System(), Models: []string{"fixture"}, Backoff: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := recovery.Ensure(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	if fixture.Snapshot().Downloads != 1 || stages(fixture) != 0 {
		t.Fatal("backoff cancellation repeated download or leaked staging")
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	before := fixture.Snapshot()
	if err := recovery.Ensure(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: %v", err)
	}
	if fixture.Snapshot().Downloads != before.Downloads {
		t.Fatal("pre-canceled recovery touched installer")
	}
}

func TestRecoveryLabIsolationRejectsEscape(t *testing.T) {
	fixture := New(t)
	if err := fixture.System().Runner.Run(context.Background(), "open", "-a", "Ollama"); err == nil {
		t.Fatal("host command accepted")
	}
	if err := fixture.System().Runner.Start("sh", "-c", "true"); err == nil {
		t.Fatal("host process accepted")
	}
	if err := fixture.System().Download(context.Background(), ollama.MacOSDownloadURL, filepath.Join(filepath.Dir(fixture.rootPath), "escape.zip")); err == nil {
		t.Fatal("out-of-root write accepted")
	}
	request, _ := http.NewRequest("GET", "http://127.0.0.1:11434/api/version", nil)
	if _, err := fixture.TransportFor(fixture.endpoint.URL).RoundTrip(request); err == nil {
		t.Fatal("host11434 socket accepted")
	}
	request, _ = http.NewRequest("GET", "https://example.invalid/download", nil)
	if _, err := fixture.TransportFor(fixture.endpoint.URL).RoundTrip(request); err == nil {
		t.Fatal("external network accepted")
	}
	if fixture.Snapshot().Denied != 5 {
		t.Fatalf("boundary refusals=%d", fixture.Snapshot().Denied)
	}
}

func TestRecoveryLabRootCleanup(t *testing.T) {
	var root string
	t.Run("scope", func(t *testing.T) {
		fixture := New(t)
		root = fixture.rootPath
		if err := fixture.Recovery(false).Ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("fixture root not cleaned: %v", err)
	}
}
