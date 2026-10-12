package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/menubar"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/recoverylab"
	"github.com/gkhntpbs/dumb/internal/server"
	"github.com/gkhntpbs/dumb/internal/worker"
)

func TestMenuLabQueuePauseResume(t *testing.T) { runMenuLab(t, false) }
func TestMenuNativePreview(t *testing.T) {
	if os.Getenv("QA_MENU_BAR_HELPER") == "" {
		t.Skip("QA_MENU_BAR_HELPER selects a transient native helper")
	}
	runMenuLab(t, true)
}

func previewDuration(raw string) (time.Duration, error) {
	if raw == "" {
		return 180 * time.Second, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 || seconds > 28800 {
		return 0, fmt.Errorf("QA_MENU_PREVIEW_SECONDS must be 1 through 28800")
	}
	return time.Duration(seconds) * time.Second, nil
}

func runMenuLab(t *testing.T, native bool) {
	duration, err := previewDuration(os.Getenv("QA_MENU_PREVIEW_SECONDS"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := recoverylab.New(t)
	credential := func() string {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(b)
	}
	wt, ct := credential(), credential()
	tokens := auth.NewStaticTokens()
	tokens.Add(credential(), auth.RoleWorker, wt)
	tokens.Add(credential(), auth.RoleCaller, ct)
	interactive := native && os.Getenv("QA_MENU_STDIN") == "1"
	maxWait, modelCheck := 20*time.Millisecond, 20*time.Millisecond
	if interactive {
		maxWait, modelCheck = 20*time.Second, 2*time.Second
	}
	service, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: maxWait})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(service)
	defer endpoint.Close()
	transport := fixture.TransportFor(endpoint.URL)
	parent := context.Background()
	if native {
		signalContext, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		defer stopSignals()
		parent = signalContext
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	controls := make(chan string, 8)
	s := &supervisor{controls: controls, log: log.New(io.Discard, "", 0)}
	var view *menuView
	var diagnosticsOffset int64
	if native {
		if info, err := os.Stat(os.Getenv("QA_MENU_DIAGNOSTICS")); err == nil {
			diagnosticsOffset = info.Size()
		}
		session, err := menubar.Start(ctx, os.Getenv("QA_MENU_BAR_HELPER"), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		view = &menuView{session: session, mode: "starting", category: "none"}
		s.controls, s.uiFailed, s.status = controls, session.Failed(), view.lifecycle
		if interactive {
			s.status = func(state, category string) {
				view.lifecycle(state, category)
				if state != "" {
					fmt.Printf("Preview state: %s\n", state)
				}
			}
		}
		if os.Getenv("QA_MENU_AUTOMATED") != "1" {
			go func() {
				for {
					select {
					case event := <-session.Events():
						select {
						case controls <- event:
						case <-ctx.Done():
							return
						}
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		if os.Getenv("QA_MENU_AUTOMATED") == "1" {
			s.controls = controls
		}
	}
	recovery := fixture.Recovery(false)
	defer recovery.Close(context.Background())
	var starts atomic.Int32
	s.newWorker = func(onHello func()) (runner, error) {
		starts.Add(1)
		cfg := worker.Config{ServerURL: endpoint.URL, Token: wt, Models: []string{"fixture"}, Version: "0.1.0", Ollama: fixture.Client(), Recovery: recovery, Transport: transport, ModelCheck: modelCheck, OnHello: onHello}
		if view != nil {
			cfg.OnStatus = view.worker
		}
		return worker.New(cfg)
	}
	result := make(chan int, 1)
	go func() { result <- s.run(ctx) }()
	await := func(label string, check func() bool) {
		limit := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(limit) {
				t.Fatal(label)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	await("ready", func() bool { return service.Status().WorkersOnline == 1 })
	submit := func() {
		b, _ := json.Marshal(protocol.SubmitRequest{Model: "fixture", Prompt: "fixture", DeadlineMS: 1000})
		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint.URL+"/v1/jobs?wait=1s", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+ct)
		res, err := (&http.Client{Transport: transport}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var job protocol.JobStatus
		if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&job) != nil || job.State != "done" {
			t.Fatal("real queue job failed")
		}
	}
	submit()
	if native && os.Getenv("QA_MENU_AUTOMATED") == "1" {
		view.lifecycle("starting", "none")
		time.Sleep(200 * time.Millisecond)
		view.lifecycle("error", "backend")
		time.Sleep(200 * time.Millisecond)
		view.lifecycle("starting", "none")
		view.lifecycle("updating", "none")
		time.Sleep(200 * time.Millisecond)
		view.lifecycle("", "none")
	}
	if native && os.Getenv("QA_MENU_AUTOMATED") != "1" {
		fmt.Println("DUMB menu preview ready")
		commands := make(chan string, 8)
		if os.Getenv("QA_MENU_STDIN") == "1" {
			go func() {
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Buffer(make([]byte, 1024), 4096)
				for scanner.Scan() {
					select {
					case commands <- string(bytes.TrimSpace(scanner.Bytes())):
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		apply := func(command string) {
			switch command {
			case "error":
				fmt.Println("Preview display: error")
				view.lifecycle("error", "backend")
			case "updating":
				fmt.Println("Preview display: updating")
				view.lifecycle("updating", "none")
			case "ready":
				fmt.Println("Preview display: ready")
				view.lifecycle("", "none")
				view.lifecycle("starting", "none")
			case "pause", "resume", "quit":
				select {
				case controls <- command:
				case <-ctx.Done():
				}
			default:
				fmt.Println("Preview input accepts ready, error, updating, pause, resume, quit")
			}
		}
		stage := os.Getenv("QA_MENU_STAGE_FILE")
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		previous := ""
		for {
			select {
			case code := <-result:
				if code != exitOK {
					t.Fatal(code)
				}
				return
			case <-ctx.Done():
				select {
				case <-result:
				case <-time.After(2 * time.Second):
					t.Fatal("preview worker did not stop")
				}
				if parent.Err() != nil {
					fmt.Println("DUMB menu preview stopped")
					return
				}
				t.Fatal("native fixture timed out")
			case command := <-commands:
				apply(command)
			case <-tick.C:
				if stage != "" {
					b, _ := os.ReadFile(stage)
					command := string(bytes.TrimSpace(b))
					if command != previous {
						previous = command
						apply(command)
					}
				}
			}
		}
	}
	controls <- "pause"
	await("pause goodbye", func() bool { return service.Status().WorkersOnline == 0 })
	if native {
		time.Sleep(200 * time.Millisecond)
	}
	controls <- "resume"
	await("resumed", func() bool { return starts.Load() == 2 && service.Status().WorkersOnline == 1 })
	submit()
	if native {
		time.Sleep(200 * time.Millisecond)
	}
	controls <- "quit"
	select {
	case code := <-result:
		if code != exitOK {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("quit not drained")
	}
	if native {
		time.Sleep(200 * time.Millisecond)
		data, err := os.ReadFile(os.Getenv("QA_MENU_DIAGNOSTICS"))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) < diagnosticsOffset {
			t.Fatal("diagnostics truncated")
		}
		data = data[diagnosticsOffset:]
		seen := map[string]bool{}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var observation struct {
				State   string `json:"state"`
				Content *int   `json:"contentWindowCount"`
				Focus   bool   `json:"focusUnchanged"`
				Active  bool   `json:"isActive"`
				Policy  string `json:"activationPolicy"`
			}
			if json.Unmarshal(line, &observation) != nil || observation.Content == nil || *observation.Content != 0 || !observation.Focus || observation.Active || observation.Policy != "accessory" {
				t.Fatalf("native surface/focus regression: %s", line)
			}
			seen[observation.State] = true
		}
		for _, state := range []string{"ready", "error", "updating", "paused"} {
			if !seen[state] {
				t.Fatalf("native short-line state update missing: %s", state)
			}
		}
	}
	snapshot := fixture.Snapshot()
	if snapshot.Denied != 0 || snapshot.Starts != 1 {
		t.Fatalf("unexpected host repair or duplicate process: %+v", snapshot)
	}
}
