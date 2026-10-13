package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopOwnedRootSurvivesEnvironmentFreeRelaunch(t *testing.T) {
	t.Setenv("DUMB_OLLAMA_DATA_DIR", "")
	root := desktopTestRoot(t)
	owned := filepath.Join(t.TempDir(), "retained-models")
	data, err := json.Marshal(map[string]string{"server": "http://127.0.0.1:18080", "models": "fixture:model", "ollama_data_dir": owned})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "token"), []byte("inert-token"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadDesktopConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ollamaSystem(nil).WindowsDataDir; got != owned {
		t.Fatalf("relaunch root = %q, want retained root %q", got, owned)
	}
}

func TestDesktopOwnedRootCompatibilityAndPrecedence(t *testing.T) {
	savedRoot := filepath.Join(t.TempDir(), "saved")
	launchRoot := filepath.Join(t.TempDir(), "launch")
	for _, tc := range []struct {
		name, saved, launch, want string
		invalid                   bool
	}{
		{name: "legacy default"},
		{name: "legacy launch override", launch: launchRoot, want: launchRoot},
		{name: "saved root", saved: savedRoot, want: savedRoot},
		{name: "explicit launch override", saved: savedRoot, launch: launchRoot, want: launchRoot},
		{name: "relative saved root", saved: "relative-root", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DUMB_OLLAMA_DATA_DIR", tc.launch)
			root := desktopTestRoot(t)
			data, err := json.Marshal(desktopConfig{Server: "http://127.0.0.1:18080", Models: "fixture:model", OllamaDataDir: tc.saved})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "token"), []byte("inert-token"), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := loadDesktopConfig(root)
			if tc.invalid {
				if err == nil {
					t.Fatal("relative saved root accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.ollamaSystem(nil).WindowsDataDir; got != tc.want {
				t.Fatalf("root = %q, want %q", got, tc.want)
			}
		})
	}
	data, err := json.Marshal(desktopConfig{Server: "http://127.0.0.1:18080", Models: "fixture:model"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["ollama_data_dir"]; present {
		t.Fatal("default root should remain implicit")
	}
}

func TestDesktopOllamaSelectionsSurviveEnvironmentFreeRelaunch(t *testing.T) {
	owned := filepath.Join(t.TempDir(), "owned")
	modelDir := filepath.Join(t.TempDir(), "models")
	t.Setenv("DUMB_OLLAMA_DATA_DIR", owned)
	t.Setenv("OLLAMA_MODELS", modelDir)
	t.Setenv("DUMB_OLLAMA", "http://127.0.0.1:19001")
	saved, err := desktopSetupConfig("http://127.0.0.1:18080", "fixture:model")
	if err != nil {
		t.Fatal(err)
	}
	root := desktopTestRoot(t)
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "token"), []byte("inert-token"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DUMB_OLLAMA_DATA_DIR", "")
	t.Setenv("OLLAMA_MODELS", "")
	t.Setenv("DUMB_OLLAMA", "")
	c, err := loadDesktopConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	system := c.ollamaSystem(nil)
	if system.WindowsDataDir != owned || system.WindowsModelsDir != modelDir || c.ollamaURL != saved.OllamaURL || !c.ollamaConfigured {
		t.Fatal("explicit Ollama selections changed after relaunch")
	}
	t.Setenv("DUMB_OLLAMA", "http://127.0.0.1:19002")
	c, err = loadDesktopConfig(root)
	if err != nil || c.ollamaURL != "http://127.0.0.1:19002" || !c.ollamaConfigured {
		t.Fatal("explicit launch endpoint lost precedence")
	}
}

func TestDesktopOllamaSelectionsKeepImplicitOwnershipAndRejectInvalidValues(t *testing.T) {
	t.Setenv("DUMB_OLLAMA_DATA_DIR", "")
	t.Setenv("OLLAMA_MODELS", "")
	t.Setenv("DUMB_OLLAMA", "")
	saved, err := desktopSetupConfig("http://127.0.0.1:18080", "fixture:model")
	if err != nil || saved.OllamaURL != "" {
		t.Fatal("default endpoint became explicit")
	}
	for _, name := range []string{"DUMB_OLLAMA_DATA_DIR"} {
		t.Setenv(name, "relative")
		if _, err := desktopSetupConfig(saved.Server, saved.Models); err == nil {
			t.Fatal("relative directory accepted")
		}
		t.Setenv(name, "")
	}
	t.Setenv("OLLAMA_MODELS", "relative-models")
	normalized, err := desktopSetupConfig(saved.Server, saved.Models)
	want, _ := filepath.Abs("relative-models")
	if err != nil || normalized.OllamaModelsDir != want {
		t.Fatal("relative model directory did not retain its original working-directory selection")
	}
	t.Setenv("DUMB_OLLAMA", "http://example.com:11434")
	if _, err := desktopSetupConfig(saved.Server, saved.Models); err == nil {
		t.Fatal("nonloopback endpoint accepted")
	}
}

func desktopTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := secureDesktopTestRoot(root); err != nil {
		t.Fatal(err)
	}
	return root
}
