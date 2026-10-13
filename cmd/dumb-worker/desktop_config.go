package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/worker"
)

type desktopConfig struct {
	Server          string `json:"server"`
	Models          string `json:"models"`
	OllamaDataDir   string `json:"ollama_data_dir,omitempty"`
	OllamaModelsDir string `json:"ollama_models_dir,omitempty"`
	OllamaURL       string `json:"ollama_url,omitempty"`
}

func loadDesktopConfig(root string) (*config, error) {
	data, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		return nil, err
	}
	var saved desktopConfig
	if err = json.Unmarshal(data, &saved); err != nil {
		return nil, errors.New("saved desktop configuration is invalid")
	}
	if _, err = worker.CheckServerURL(saved.Server); err != nil {
		return nil, err
	}
	c, _, err := parseFlags("run", nil, io.Discard)
	if err != nil {
		return nil, err
	}
	if saved.OllamaDataDir != "" && !filepath.IsAbs(saved.OllamaDataDir) {
		return nil, errors.New("saved Ollama data directory must be absolute")
	}
	if saved.OllamaModelsDir != "" && !filepath.IsAbs(saved.OllamaModelsDir) {
		return nil, errors.New("saved Ollama model directory must be absolute")
	}
	if saved.OllamaURL != "" {
		if _, err := ollama.New(saved.OllamaURL); err != nil {
			return nil, err
		}
		if !c.ollamaConfigured {
			c.ollamaURL = saved.OllamaURL
			c.ollamaConfigured = true
		}
	}
	c.ollamaDataDir = saved.OllamaDataDir
	c.ollamaModelsDir = saved.OllamaModelsDir
	c.server = saved.Server
	c.models = saved.Models
	c.tokenFile = filepath.Join(root, "token")
	if len(c.modelList()) == 0 {
		return nil, errors.New("saved configuration has no models")
	}
	if _, err = worker.ReadTokenFile(c.tokenFile); err != nil {
		return nil, err
	}
	return c, nil
}

func desktopSetupConfig(server, models string) (desktopConfig, error) {
	saved := desktopConfig{Server: server, Models: models, OllamaDataDir: os.Getenv("DUMB_OLLAMA_DATA_DIR"), OllamaModelsDir: os.Getenv("OLLAMA_MODELS"), OllamaURL: os.Getenv("DUMB_OLLAMA")}
	if saved.OllamaDataDir != "" && !filepath.IsAbs(saved.OllamaDataDir) {
		return desktopConfig{}, errors.New("Ollama data directory must be absolute")
	}
	if saved.OllamaModelsDir != "" {
		var err error
		saved.OllamaModelsDir, err = filepath.Abs(saved.OllamaModelsDir)
		if err != nil {
			return desktopConfig{}, err
		}
	}
	if _, err := ollama.New(env("DUMB_OLLAMA", ollama.DefaultURL)); err != nil {
		return desktopConfig{}, err
	}
	return saved, nil
}

type desktopRuntimeInfo struct {
	Version   int    `json:"version"`
	Nonce     string `json:"nonce"`
	WorkerPID int    `json:"worker_pid"`
	OllamaPID int    `json:"ollama_pid"`
	OllamaURL string `json:"ollama_url"`
}

func desktopStatusObserver(next func(worker.Status), ready func()) func(worker.Status) {
	return func(status worker.Status) {
		if next != nil {
			next(status)
		}
		if status.Ready && !status.Error {
			ready()
		}
	}
}
