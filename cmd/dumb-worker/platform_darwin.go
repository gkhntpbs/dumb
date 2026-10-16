package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/service"
	"github.com/gkhntpbs/dumb/internal/worker"
)

type macSetupInput struct {
	Server string `json:"server"`
	Models string `json:"models"`
	Error  string `json:"error"`
}

type macSetupResult struct {
	Server         string `json:"server"`
	Models         string `json:"models"`
	Credential     string `json:"credential"`
	Authentication string `json:"authentication"`
	StartAtLogin   bool   `json:"start_at_login"`
}

func macRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "DUMB", "Worker"), nil
}

func macLoadConfig(root string) (*config, error) {
	info, err := os.Lstat(filepath.Join(root, "config.json"))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("worker configuration must be a private regular file")
	}
	return loadDesktopConfig(root)
}

func macPrivateRoot(root string) error {
	for path := root; filepath.Dir(path) != path; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("worker directory cannot contain symbolic links or special files")
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	return os.Chmod(root, 0700)
}

type macDialogOutput struct{ bytes.Buffer }

func (output *macDialogOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 16384 {
		return 0, errors.New("setup response exceeds its limit")
	}
	return output.Buffer.Write(data)
}

func macDialog(ctx context.Context, mode string, input any, output any) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	helper, err := menuHelper(exe)
	if err != nil {
		return err
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, helper, mode)
	cmd.Stdin = bytes.NewReader(data)
	var result macDialogOutput
	cmd.Stdout = &result
	if err := cmd.Run(); err != nil {
		return err
	}
	if output == nil {
		return nil
	}
	if result.Len() > 16384 {
		return errors.New("setup response exceeds its limit")
	}
	decoder := json.NewDecoder(&result.Buffer)
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

func macCredential(ctx context.Context, server, token string) error {
	return macCredentialWith(ctx, &http.Client{Timeout: 15 * time.Second}, server, token)
}

func macCredentialWith(ctx context.Context, client *http.Client, server, token string) error {
	endpoint, err := worker.CheckServerURL(server)
	if err != nil {
		return err
	}
	if endpoint.Scheme != "https" {
		return errors.New("desktop setup requires an HTTPS server URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.JoinPath("/v1/workers/me").String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(protocol.WorkerVersionHeader, version)
	safe := *client
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safe.Do(req)
	if err != nil {
		return errors.New("server connection failed; check the URL and network")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("worker credential/version check failed (HTTP %d)", response.StatusCode)
	}
	var identity protocol.WhoAmI
	if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&identity) != nil || identity.WorkerID == "" {
		return errors.New("server returned an invalid worker identity")
	}
	return nil
}

func macSave(root string, result macSetupResult, token string) error {
	saved, err := desktopSetupConfig(result.Server, result.Models)
	if err != nil {
		return err
	}
	if len((&config{models: result.Models}).modelList()) == 0 {
		return errors.New("enter at least one Ollama model")
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	for _, name := range []string{"config.json", "token"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("worker configuration already exists; refusing to overwrite it")
		}
	}
	if err := storePairToken(filepath.Join(root, "token"), token); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(root, "config.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.Remove(filepath.Join(root, "token"))
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(filepath.Join(root, "config.json"))
		os.Remove(filepath.Join(root, "token"))
	}
	return err
}

type macSetupActions struct {
	dialog   func(context.Context, string, any, any) error
	exchange func(context.Context, string, string) (string, error)
	verify   func(context.Context, string, string) error
	save     func(string, macSetupResult, string) error
}

func macSetup(ctx context.Context, root string) (bool, error) {
	return macSetupWith(ctx, root, macSetupActions{dialog: macDialog, exchange: func(ctx context.Context, server, code string) (string, error) {
		return exchangePair(ctx, http.DefaultClient, server, code)
	}, verify: macCredential, save: macSave})
}

func macSetupWith(ctx context.Context, root string, actions macSetupActions) (bool, error) {
	input := macSetupInput{Server: os.Getenv("DUMB_SERVER"), Models: os.Getenv("DUMB_MODELS")}
	for {
		var result macSetupResult
		if err := actions.dialog(ctx, "--setup", input, &result); err != nil {
			return false, err
		}
		input.Server, input.Models = result.Server, result.Models
		token := strings.TrimSpace(result.Credential)
		_, err := worker.CheckServerURL(result.Server)
		if err == nil && len((&config{models: result.Models}).modelList()) == 0 {
			err = errors.New("enter at least one Ollama model")
		}
		if err == nil {
			switch result.Authentication {
			case "pairing":
				token, err = actions.exchange(ctx, result.Server, token)
			case "token":
				if token == "" || strings.ContainsAny(token, "\r\n\x00") {
					err = errors.New("enter a valid worker token")
				}
			default:
				err = errors.New("select pairing code or worker token")
			}
		}
		if err == nil {
			for {
				err = actions.verify(ctx, result.Server, token)
				if err == nil {
					err = actions.save(root, result, token)
				}
				if err == nil {
					return result.StartAtLogin, nil
				}
				var choice struct {
					Retry bool `json:"retry"`
				}
				if retryErr := actions.dialog(ctx, "--retry", macSetupInput{Error: err.Error()}, &choice); retryErr != nil {
					return false, retryErr
				}
				if !choice.Retry {
					return false, errors.New("setup cancelled")
				}
			}
		}
		input.Error = err.Error()
	}
}

func macStartup(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cfg := service.Config{GOOS: "darwin", Label: "com.dumb.worker.desktop", Binary: exe, Home: home, UID: os.Getuid()}
	path := service.Path(cfg)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("a desktop startup entry already exists; refusing to replace it")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	plist := strings.Replace(service.Plist(cfg), "<string>run</string>", "<string>desktop</string>", 1)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.WriteString(plist)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	cmd := exec.CommandContext(ctx, "launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), path)
	if err := cmd.Run(); err != nil {
		os.Remove(path)
		return errors.New("worker startup registration failed")
	}
	return nil
}

func platformRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] != "desktop" && args[0] != "desktop-worker" {
		return run(ctx, args, stdout, stderr)
	}
	exe, err := os.Executable()
	if err != nil {
		return exitError
	}
	if _, err = menuHelper(exe); err != nil {
		return run(ctx, args, stdout, stderr)
	}
	root, err := macRoot()
	if err == nil {
		err = macPrivateRoot(root)
	}
	if err != nil {
		macDialog(ctx, "--message", macSetupInput{Error: "Cannot open the private worker configuration directory."}, nil)
		return exitError
	}
	if len(args) > 0 && args[0] == "desktop-worker" {
		c, err := macLoadConfig(root)
		if err != nil {
			return exitError
		}
		file, err := os.OpenFile(filepath.Join(root, "worker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return exitError
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return exitError
		}
		return cmdRun(ctx, c, log.New(file, "dumb-worker: ", log.LstdFlags))
	}
	lock, err := os.OpenFile(filepath.Join(root, "desktop.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return exitError
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return exitOK
	}
	if _, err := macLoadConfig(root); err != nil {
		_, configErr := os.Lstat(filepath.Join(root, "config.json"))
		if !errors.Is(err, os.ErrNotExist) || !errors.Is(configErr, os.ErrNotExist) {
			macDialog(ctx, "--message", macSetupInput{Error: "Saved worker configuration is invalid. Restore or remove it before setup."}, nil)
			return exitError
		}
		startup, err := macSetup(ctx, root)
		if err != nil {
			return exitOK
		}
		if startup {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			if err := macStartup(ctx); err != nil {
				macDialog(ctx, "--message", macSetupInput{Error: err.Error() + ". The worker will run for this session."}, nil)
				if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
					return exitOK
				}
			} else {
				return exitOK
			}
		}
	}
	for {
		cmd := exec.CommandContext(ctx, exe, "desktop-worker")
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 20 * time.Second
		err := cmd.Run()
		if err == nil {
			return exitOK
		}
		var exited *exec.ExitError
		if errors.As(err, &exited) && exited.ExitCode() == exitRestart && ctx.Err() == nil {
			continue
		}
		return exitError
	}
}

func platformReady(...desktopRuntimeInfo) {}
