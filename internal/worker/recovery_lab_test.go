package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/auth"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/recoverylab"
	"github.com/gkhntpbs/dumb/internal/server"
)

type workerRecoveryLab struct {
	fixture   *recoverylab.Fixture
	server    *server.Server
	endpoint  *httptest.Server
	worker    *Worker
	caller    string
	transport http.RoundTripper
	context   context.Context
	cancel    context.CancelFunc
	done      chan error
}

func newWorkerRecoveryLab(t *testing.T, fixture *recoverylab.Fixture, external, badCredential bool) *workerRecoveryLab {
	t.Helper()
	credential := func() string {
		buffer := make([]byte, 24)
		if _, err := rand.Read(buffer); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(buffer)
	}
	workerToken, callerToken := credential(), credential()
	tokens := auth.NewStaticTokens()
	tokens.Add(credential(), auth.RoleWorker, workerToken)
	tokens.Add(credential(), auth.RoleCaller, callerToken)
	service, err := server.New(server.Config{Auth: tokens, MinWorkerVersion: "0.1.0", MaxWait: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewServer(service)
	t.Cleanup(endpoint.Close)
	transport := fixture.TransportFor(endpoint.URL)
	if badCredential {
		workerToken = credential()
	}
	w, err := New(Config{ServerURL: endpoint.URL, Token: workerToken, Models: []string{"fixture"}, Version: "0.1.0", Ollama: fixture.Client(), Recovery: fixture.Recovery(external), Transport: transport, ModelCheck: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	w.backoff, w.maxBack = time.Millisecond, 5*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	lab := &workerRecoveryLab{fixture: fixture, server: service, endpoint: endpoint, worker: w, caller: callerToken, transport: transport, context: ctx, cancel: cancel, done: make(chan error, 1)}
	t.Cleanup(cancel)
	return lab
}

func (lab *workerRecoveryLab) run(t *testing.T) {
	t.Helper()
	go func() { lab.done <- lab.worker.Run(lab.context) }()
	t.Cleanup(func() {
		lab.cancel()
		select {
		case <-lab.done:
		case <-time.After(time.Second):
			t.Error("sealed worker did not stop")
		}
	})
}

func (lab *workerRecoveryLab) submit(t *testing.T) protocol.JobStatus {
	t.Helper()
	body, err := json.Marshal(protocol.SubmitRequest{Model: "fixture", Prompt: "fixture prompt", DeadlineMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, lab.endpoint.URL+"/v1/jobs?wait=1s", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+lab.caller)
	response, err := (&http.Client{Transport: lab.transport}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("job response status %d", response.StatusCode)
	}
	var job protocol.JobStatus
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestRecoveryLabWorkerReadinessServesJob(t *testing.T) {
	for _, installed := range []bool{false, true} {
		name := "absent"
		if installed {
			name = "installed-stopped"
		}
		t.Run(name, func(t *testing.T) {
			fixture := recoverylab.New(t)
			if installed {
				fixture.InstallPresent()
			}
			lab := newWorkerRecoveryLab(t, fixture, false, false)
			lab.run(t)
			eventually(t, "sealed worker ready", func() bool { return lab.server.Status().WorkersOnline == 1 })
			job := lab.submit(t)
			if job.State != "done" || job.Output != "fixture answer" {
				t.Fatalf("job after recovery: %+v", job)
			}
			snapshot := fixture.Snapshot()
			if snapshot.Starts != 1 || snapshot.Denied != 0 || snapshot.DiskBytes > 64<<10 {
				t.Fatalf("unsafe or excessive fixture operations: %+v", snapshot)
			}
			if installed && snapshot.Downloads != 0 || !installed && snapshot.Downloads != 1 {
				t.Fatalf("unexpected installer operations: %+v", snapshot)
			}
		})
	}
}

func TestRecoveryLabGenerateFailureRestartsOwnedProcess(t *testing.T) {
	fixture := recoverylab.New(t)
	fixture.InstallPresent()
	fixture.AddModel("fixture")
	lab := newWorkerRecoveryLab(t, fixture, false, false)
	lab.run(t)
	eventually(t, "owned worker ready", func() bool { return lab.server.Status().WorkersOnline == 1 })
	fixture.Fault("generate_unavailable", 1)
	job := lab.submit(t)
	if job.State != "failed" || job.Error == "" {
		t.Fatalf("fault was not reproduced: %+v", job)
	}
	eventually(t, "owned restart despite healthy model list", func() bool {
		snapshot := fixture.Snapshot()
		return snapshot.Starts == 2 && snapshot.Stops == 1 && lab.server.Status().WorkersOnline == 1
	})
	job = lab.submit(t)
	if job.State != "done" || job.Output != "fixture answer" {
		t.Fatalf("subsequent job did not recover: %+v", job)
	}
	if snapshot := fixture.Snapshot(); snapshot.Denied != 0 || snapshot.Downloads != 0 {
		t.Fatalf("unexpected host/install operations: %+v", snapshot)
	}
}

func TestRecoveryLabExternalFailureNeverStartsOrInstalls(t *testing.T) {
	fixture := recoverylab.New(t)
	fixture.SetRunning(true)
	fixture.AddModel("fixture")
	lab := newWorkerRecoveryLab(t, fixture, true, false)
	lab.run(t)
	eventually(t, "external worker ready", func() bool { return lab.server.Status().WorkersOnline == 1 })
	fixture.SetRunning(false)
	eventually(t, "external worker withdrawn", func() bool { return lab.server.Status().WorkersOnline == 0 })
	fixture.SetRunning(true)
	eventually(t, "external owner restored readiness", func() bool { return lab.server.Status().WorkersOnline == 1 })
	if job := lab.submit(t); job.State != "done" {
		t.Fatalf("restored proxy job: %+v", job)
	}
	if snapshot := fixture.Snapshot(); snapshot.Starts != 0 || snapshot.Stops != 0 || snapshot.Downloads != 0 || snapshot.Denied != 0 {
		t.Fatalf("unmanaged endpoint was modified: %+v", snapshot)
	}
}

func TestRecoveryLabBadCredentialDoesNotRepair(t *testing.T) {
	fixture := recoverylab.New(t)
	lab := newWorkerRecoveryLab(t, fixture, false, true)
	err := lab.worker.Run(lab.context)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad credential result: %v", err)
	}
	if snapshot := fixture.Snapshot(); snapshot.Starts != 0 || snapshot.Downloads != 0 || snapshot.Extracts != 0 {
		t.Fatalf("unauthorized worker repaired the system: %+v", snapshot)
	}
}
