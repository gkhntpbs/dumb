//go:build windows

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gkhntpbs/dumb/internal/ollama"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/worker"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func desktopRoot() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(root) {
		return "", errors.New("LOCALAPPDATA must be an absolute directory")
	}
	return filepath.Join(root, "DUMB", "Worker"), nil
}

func restrictDesktopPath(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, acl, nil)
}

func desktopCredential(ctx context.Context, server, token string) error {
	endpoint, err := worker.CheckServerURL(server)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.JoinPath("/v1/workers/me").String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(protocol.WorkerVersionHeader, version)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("server connection failed; check the URL and network")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("worker credential/version check failed (HTTP %d)", res.StatusCode)
	}
	var me protocol.WhoAmI
	if json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&me) != nil || me.WorkerID == "" {
		return errors.New("server returned an invalid worker identity")
	}
	return nil
}

func setupConsole() (*os.File, *os.File, error) {
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	allocated, _, err := kernel.NewProc("AllocConsole").Call()
	if allocated == 0 {
		return nil, nil, fmt.Errorf("open setup console: %w", err)
	}
	kernel.NewProc("SetConsoleOutputCP").Call(65001)
	input, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, err
	}
	output, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		input.Close()
		return nil, nil, err
	}
	return input, output, nil
}

func desktopSetup(ctx context.Context, root string) int {
	mutex, err := windows.CreateMutex(nil, false, desktopObject(root, "setup"))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if mutex != 0 {
			windows.CloseHandle(mutex)
		}
		return exitOK
	}
	if err != nil {
		return exitError
	}
	defer windows.CloseHandle(mutex)
	input, output, err := setupConsole()
	if err != nil {
		return exitError
	}
	defer input.Close()
	defer output.Close()
	read := func(label string, secret bool) (string, error) { return setupNativeRead(input, output, label, secret) }
	display := &setupDisplay{output: output, columns: func() int { return setupConsoleColumns(output) }}
	display.note("DUMB WORKER - FIRST-RUN SETUP")
	display.note("Seven steps connect this computer and prepare a tray worker.")
	display.note("Paste with Ctrl+V or right-click; press Enter to confirm each field.")
	display.note("Type :exit at a prompt to stop. Errors keep this window open.")
	for {
		server, token, err := setupCredentials(read, display, func(value string) error { _, err := worker.CheckServerURL(value); return err }, func(server, token string) error { return desktopCredential(ctx, server, token) })
		if errors.Is(err, errSetupExit) {
			return exitOK
		}
		if err != nil {
			if setupRetry(read, display, err) {
				continue
			}
			return exitError
		}
		display.note("\n[3/7] Models to run")
		display.note("Enter Ollama model names, separated by commas. Example: smollm2:135m")
		models, err := setupValidated(read, display, "Ollama models (comma-separated)", false, func(value string) error {
			if len((&config{models: value}).modelList()) == 0 {
				return errors.New("Enter at least one model name")
			}
			return nil
		})
		if errors.Is(err, errSetupExit) {
			return exitOK
		}
		if err == nil {
			err = desktopFinishSetup(ctx, root, server, token, models, display)
		}
		if err != nil {
			if setupRetry(read, display, err) {
				continue
			}
			return exitError
		}
		display.note("\nYou may close this setup terminal. The worker stays in the tray.")
		read("Press Enter to close this setup terminal", false)
		return exitOK
	}
}

func desktopFinishSetup(ctx context.Context, root, server, token, models string, display *setupDisplay) error {
	for path := root; filepath.Dir(path) != path; path = filepath.Dir(path) {
		if info, e := os.Lstat(path); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("setup directory cannot contain symbolic links or junctions")
		}
	}
	if err := desktopStartupAvailable(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	if err := restrictDesktopPath(root); err != nil {
		return err
	}
	client, err := ollama.New(env("DUMB_OLLAMA", ollama.DefaultURL))
	if err != nil {
		return err
	}
	saved, err := desktopSetupConfig(server, models)
	if err != nil {
		return err
	}
	system := ollama.NewSystem(client)
	display.note("\n[4/7] Ollama and model preparation")
	display.note("Ollama is checked only on a loopback endpoint: %s", env("DUMB_OLLAMA", ollama.DefaultURL))
	recovery, err := ollama.NewRecovery(ollama.RecoveryConfig{System: system, Models: (&config{models: models}).modelList(), External: os.Getenv("DUMB_OLLAMA") != "", Logger: log.New(io.Discard, "", 0), Progress: func(p ollama.RecoveryProgress) {
		switch p.Stage {
		case "checking":
			display.set("Checking the local Ollama API")
		case "reachable":
			display.note("Existing Ollama API found and reachable; preserving its process.")
		case "detecting":
			display.set("Looking for an installed Ollama CLI")
		case "found":
			display.note("Installed Ollama CLI found; no download is needed.")
		case "installing":
			display.note("Ollama is not installed. Downloading and installing the official package.")
			display.set("Downloading official Ollama - waiting for network/archive")
		case "installed":
			display.note("Official Ollama download and installation completed.")
		case "starting":
			display.set("Starting Ollama in the background; waiting for its API")
		case "api-ready":
			if pid := system.OwnedPID(); pid > 0 {
				display.note("Ollama background process started: PID %d. API is ready.", pid)
			} else {
				display.note("Existing external Ollama API is ready.")
			}
		case "model-ready":
			display.note("Model available: %s", p.Model)
		case "pulling":
			if p.Total > 0 {
				display.set(fmt.Sprintf("Model %s: %d/%d bytes (current layer)", p.Model, p.Completed, p.Total))
			} else {
				display.set("Downloading/preparing model " + p.Model)
			}
		case "ready":
			display.note("Ollama API and all configured models are ready.")
		}
	}})
	if err != nil {
		return err
	}
	err = display.operation("Preparing Ollama and configured models", func() error { return recovery.Ensure(ctx) })
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	closeErr := recovery.Close(closeCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("Ollama preparation failed: %w", err)
	}
	if closeErr != nil {
		return errors.New("setup's owned Ollama process could not stop for handoff")
	}
	display.note("\n[5/7] Installing this user's worker")
	if err = installDesktop(root, saved, token); err != nil {
		return err
	}
	var info desktopRuntimeInfo
	display.note("Handing off to the background worker; rechecking Ollama, tray and server readiness.")
	err = display.operation("Starting background worker, tray and server hello", func() error {
		var err error
		info, err = startDesktopProcess(root, true)
		if err != nil {
			return err
		}
		return desktopVerifyRuntime(ctx, info, models)
	})
	if err != nil {
		rollbackDesktop(root)
		return err
	}
	display.note("\n[6/7] Startup at Windows sign-in")
	if err = display.operation("Registering worker startup for this user", func() error { return registerDesktopStartup(root) }); err != nil {
		rollbackDesktop(root)
		return err
	}
	display.note("\n[7/7] ALL CHECKS PASSED")
	display.note("Worker running: PID %d | Tray ready | Server hello confirmed", info.WorkerPID)
	if info.OllamaPID > 0 {
		display.note("Owned Ollama running in background: PID %d", info.OllamaPID)
	} else {
		display.note("Ollama is provided by the existing external process.")
	}
	display.note("Ollama API: %s | Ready | Models: %s", info.OllamaURL, models)
	display.note("Startup: enabled for this user's Windows sign-in.")
	return nil
}

func desktopVerifyRuntime(ctx context.Context, info desktopRuntimeInfo, models string) error {
	for _, pid := range []int{info.WorkerPID, info.OllamaPID} {
		if pid == 0 {
			continue
		}
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			return errors.New("background process exited before final readiness")
		}
		result, err := windows.WaitForSingleObject(handle, 0)
		windows.CloseHandle(handle)
		if err != nil || result == windows.WAIT_OBJECT_0 {
			return errors.New("background process exited before final readiness")
		}
	}
	client, err := ollama.New(info.OllamaURL)
	if err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	available, err := client.List(probe)
	if err != nil {
		return errors.New("background Ollama API is not ready; retry setup")
	}
	for _, model := range (&config{models: models}).modelList() {
		if !ollama.HasModel(available, model) {
			return errors.New("background Ollama is missing a configured model")
		}
	}
	return nil
}

func desktopWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		os.Remove(path)
		return err
	}
	if closeErr != nil {
		os.Remove(path)
	}
	return closeErr
}

func installDesktop(root string, saved desktopConfig, token string) error {
	for _, name := range []string{"dumb-worker.exe", "token", "config.json"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("an installed worker or configuration already exists; refusing to overwrite it")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	created := []string{}
	success := false
	defer func() {
		if !success {
			for _, path := range created {
				os.Remove(path)
			}
		}
	}()
	write := func(name string, data []byte, mode os.FileMode) error {
		path := filepath.Join(root, name)
		if err := desktopWrite(path, data, mode); err != nil {
			return err
		}
		created = append(created, path)
		return nil
	}
	if err = write("dumb-worker.exe", data, 0700); err != nil {
		return err
	}
	if err = write("token", []byte(token), 0600); err != nil {
		return err
	}
	if err = restrictDesktopPath(filepath.Join(root, "token")); err != nil {
		return err
	}
	data, err = json.Marshal(saved)
	if err != nil {
		return err
	}
	if err = write("config.json", data, 0600); err != nil {
		return err
	}
	success = true
	return nil
}

func desktopStartupAvailable(root string) error {
	command := syscall.EscapeArg(filepath.Join(root, "dumb-worker.exe")) + " desktop"
	if len(command) > 260 {
		return errors.New("installed startup command exceeds the Windows Run limit")
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()
	previous, _, err := key.GetStringValue("DUMB Worker")
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if previous != command {
		return errors.New("DUMB Worker startup entry already belongs to another installation")
	}
	return nil
}

func registerDesktopStartup(root string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	command := syscall.EscapeArg(filepath.Join(root, "dumb-worker.exe")) + " desktop"
	if len(command) > 260 {
		return errors.New("installed startup command exceeds the Windows Run limit")
	}
	previous, _, err := key.GetStringValue("DUMB Worker")
	if err == nil && previous != command {
		return errors.New("DUMB Worker startup entry already belongs to another installation")
	}
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return key.SetStringValue("DUMB Worker", command)
}

func desktopObject(root, kind string) *uint16 {
	sum := sha256.Sum256([]byte(strings.ToLower(root)))
	value, _ := windows.UTF16PtrFromString("Local\\DUMB-" + kind + "-" + hex.EncodeToString(sum[:16]))
	return value
}

func startDesktop(root string, wait bool) error {
	_, err := startDesktopProcess(root, wait)
	return err
}

func startDesktopProcess(root string, wait bool) (desktopRuntimeInfo, error) {
	entropy := make([]byte, 16)
	if _, err := rand.Read(entropy); err != nil {
		return desktopRuntimeInfo{}, err
	}
	nonce := hex.EncodeToString(entropy)
	ready := filepath.Join(root, "ready-"+nonce)

	source := filepath.Join(root, "dumb-worker.exe")
	data, err := os.ReadFile(source)
	if err != nil {
		return desktopRuntimeInfo{}, err
	}
	supervisor := filepath.Join(root, "supervisor-"+nonce+".exe")
	if err := desktopWrite(supervisor, data, 0700); err != nil {
		return desktopRuntimeInfo{}, err
	}
	cmd := exec.Command(supervisor, "desktop", nonce)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		os.Remove(supervisor)
		return desktopRuntimeInfo{}, err
	}
	defer cmd.Process.Release()
	if !wait {
		return desktopRuntimeInfo{}, nil
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(ready); err == nil && string(data) == nonce {
			data, err := os.ReadFile(filepath.Join(root, "runtime-"+nonce+".json"))
			if err != nil {
				return desktopRuntimeInfo{}, errors.New("background worker readiness details are missing")
			}
			var info desktopRuntimeInfo
			if json.Unmarshal(data, &info) != nil || info.Version != 1 || info.Nonce != nonce || info.WorkerPID <= 0 || info.OllamaPID < 0 {
				return desktopRuntimeInfo{}, errors.New("background worker readiness details are invalid")
			}
			return info, nil
		}
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
		if err != nil {
			return desktopRuntimeInfo{}, errors.New("desktop supervisor could not start")
		}
		result, _ := windows.WaitForSingleObject(handle, 0)
		windows.CloseHandle(handle)
		if result == windows.WAIT_OBJECT_0 {
			return desktopRuntimeInfo{}, errors.New("desktop supervisor exited before readiness")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return desktopRuntimeInfo{}, errors.New("tray worker did not confirm server hello and UI readiness")
}

func rollbackDesktop(root string) {
	event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, desktopObject(root, "stop"))
	if err == nil {
		windows.SetEvent(event)
		windows.CloseHandle(event)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, desktopObject(root, "desktop"))
		if err != nil {
			break
		}
		windows.CloseHandle(handle)
		time.Sleep(100 * time.Millisecond)
	}
	copies, _ := filepath.Glob(filepath.Join(root, "supervisor-*.exe"))
	for _, path := range copies {
		os.Remove(path)
	}
	for _, name := range []string{"config.json", "token", "dumb-worker.exe"} {
		os.Remove(filepath.Join(root, name))
	}
}

func desktopSupervisor(ctx context.Context, root, nonce string) int {
	if nonce == "" {
		entropy := make([]byte, 16)
		if _, err := rand.Read(entropy); err != nil {
			return exitError
		}
		nonce = hex.EncodeToString(entropy)
	}
	if len(nonce) != 32 {
		return exitUsage
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return exitUsage
	}
	defer os.Remove(filepath.Join(root, "ready-"+nonce))
	defer os.Remove(filepath.Join(root, "runtime-"+nonce+".json"))
	mutex, err := windows.CreateMutex(nil, false, desktopObject(root, "desktop"))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if mutex != 0 {
			windows.CloseHandle(mutex)
		}
		return exitOK
	}
	if err != nil {
		return exitError
	}
	defer windows.CloseHandle(mutex)
	event, err := windows.CreateEvent(nil, 1, 0, desktopObject(root, "stop"))
	if err != nil {
		return exitError
	}
	defer windows.CloseHandle(event)
	for {
		os.Remove(filepath.Join(root, "ready-"+nonce))
		os.Remove(filepath.Join(root, "runtime-"+nonce+".json"))
		child, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(child, filepath.Join(root, "dumb-worker.exe"), "desktop-worker")
		cmd.Env = append(os.Environ(), "DUMB_DESKTOP_READY="+nonce)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		if err := cmd.Start(); err != nil {
			cancel()
			return exitError
		}
		finished := make(chan error, 1)
		go func() { finished <- cmd.Wait() }()
		ticker := time.NewTicker(100 * time.Millisecond)
		deadline := time.Now().Add(60 * time.Second)
		stopped := false
		for {
			select {
			case err := <-finished:
				ticker.Stop()
				cancel()
				var exit *exec.ExitError
				if err == nil {
					return exitOK
				}
				if errors.As(err, &exit) {
					if exit.ExitCode() == exitRestart && !stopped && ctx.Err() == nil {
						goto restart
					}
					return exit.ExitCode()
				}
				return exitError
			case <-ticker.C:
				if nonce != "" && !stopped {
					if data, err := os.ReadFile(filepath.Join(root, "ready-"+nonce)); err == nil && string(data) == nonce {
						deadline = time.Time{}
					}
				}
				if !deadline.IsZero() && time.Now().After(deadline) {
					cancel()
					<-finished
					ticker.Stop()
					return exitError
				}
				if result, _ := windows.WaitForSingleObject(event, 0); result == windows.WAIT_OBJECT_0 && !stopped {
					stopped = true
					deadline = time.Now().Add(10 * time.Second)
				}
			case <-ctx.Done():
				cancel()
				<-finished
				ticker.Stop()
				return exitError
			}
		}
	restart:
	}
}

func desktopWorker(ctx context.Context, root string) int {
	c, err := loadDesktopConfig(root)
	if err != nil {
		return exitError
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	event, err := windows.OpenEvent(windows.SYNCHRONIZE, false, desktopObject(root, "stop"))
	if err != nil {
		return exitError
	}
	defer windows.CloseHandle(event)
	go func() {
		for {
			select {
			case <-child.Done():
				return
			case <-time.After(100 * time.Millisecond):
				if result, _ := windows.WaitForSingleObject(event, 0); result == windows.WAIT_OBJECT_0 {
					cancel()
					return
				}
			}
		}
	}()
	logfile, err := os.OpenFile(filepath.Join(root, "worker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return exitError
	}
	defer logfile.Close()
	return cmdRun(child, c, log.New(logfile, "dumb-worker: ", log.LstdFlags))
}

func platformReady(details ...desktopRuntimeInfo) {
	root, err := desktopRoot()
	if err != nil {
		return
	}
	nonce := os.Getenv("DUMB_DESKTOP_READY")
	if len(os.Args) > 1 && os.Args[1] == "desktop-worker" && len(nonce) == 32 {
		if _, err := hex.DecodeString(nonce); err == nil {
			info := desktopRuntimeInfo{Version: 1, Nonce: nonce, WorkerPID: os.Getpid()}
			if len(details) > 0 {
				info.OllamaPID = details[0].OllamaPID
				info.OllamaURL = details[0].OllamaURL
			}
			data, err := json.Marshal(info)
			if err != nil {
				return
			}
			if err = desktopWrite(filepath.Join(root, "runtime-"+nonce+".json"), data, 0600); err != nil && !errors.Is(err, os.ErrExist) {
				return
			}
			desktopWrite(filepath.Join(root, "ready-"+nonce), []byte(nonce), 0600)
		}
	}
}
