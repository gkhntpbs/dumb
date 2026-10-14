package queue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func boundedQueue(t *testing.T, limits Limits) *Queue {
	t.Helper()
	q, err := NewWithLimits(limits)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func smallLimits() Limits { return Limits{4, 8, 8 << 20, 2, 4, 4 << 20} }

func TestAdmissionConcurrentCallerAndGlobalCounts(t *testing.T) {
	q := boundedQueue(t, smallLimits())
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := map[string]int{}
	for _, owner := range []string{"a", "b", "c"} {
		for range 20 {
			wg.Add(1)
			go func(owner string) {
				defer wg.Done()
				id, err := q.Submit(owner, req(time.Minute))
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					accepted[owner]++
					if id == "" {
						t.Error("accepted empty ID")
					}
				} else if id != "" || (!errors.Is(err, ErrCallerCapacity) && !errors.Is(err, ErrCapacity)) {
					t.Errorf("rejection allocated ID: %q %v", id, err)
				}
			}(owner)
		}
	}
	wg.Wait()
	if q.total.active != 4 || q.total.jobs != 4 || len(q.jobs) != 4 || len(q.pending) != 4 {
		t.Fatalf("global boundary: %+v jobs%d pending%d", q.total, len(q.jobs), len(q.pending))
	}
	for owner, count := range accepted {
		if count > 2 || q.callers[owner].active != count {
			t.Fatalf("caller boundary: %s %d", owner, count)
		}
	}
}

func TestAdmissionUTF8ByteBoundaryAndNoAllocation(t *testing.T) {
	request := protocol.SubmitRequest{Model: "m", Prompt: "ü猫", System: "é", Format: "json", DeadlineMS: 60000}
	bytes := requestBytes("a", request.Model, request.Prompt, request.System, request.Format) + MaxResultBytes
	q := boundedQueue(t, Limits{4, 8, 2 * bytes, 2, 4, bytes})
	id, err := q.Submit("a", request)
	if err != nil || id == "" || q.total.bytes != bytes {
		t.Fatalf("exact boundary: %q %v %+v", id, err, q.total)
	}
	before := q.total
	rejected, err := q.Submit("a", request)
	if rejected != "" || !errors.Is(err, ErrCallerCapacity) || q.total != before || len(q.jobs) != 1 || len(q.pending) != 1 {
		t.Fatalf("rejection changed queue: %q %v %+v", rejected, err, q.total)
	}
	if _, err = q.Submit("b", request); err != nil {
		t.Fatal(err)
	}
	if id, err = q.Submit("c", request); id != "" || !errors.Is(err, ErrCapacity) {
		t.Fatalf("global bytes: %q %v", id, err)
	}
	if _, exists := q.callers["c"]; exists {
		t.Fatal("rejected caller allocated accounting")
	}
}

func TestTerminalRetentionReleasesPromptReservationAndCaller(t *testing.T) {
	for _, state := range []string{protocol.StateDone, protocol.StateFailed, protocol.StateExpired} {
		t.Run(state, func(t *testing.T) {
			q := boundedQueue(t, smallLimits())
			now := time.Now()
			q.SetClock(func() time.Time { return now })
			request := req(time.Minute)
			request.Prompt = strings.Repeat("p", 128)
			request.System = "private system"
			id, err := q.Submit("a", request)
			if err != nil {
				t.Fatal(err)
			}
			j := q.jobs[id]
			if state == protocol.StateExpired {
				now = now.Add(time.Minute)
				_, _ = q.Deadline("a", id)
			} else {
				assignment := claimNow(t, q, "w")
				output, errMsg := "result", ""
				if state == protocol.StateFailed {
					output = ""
					errMsg = "failed"
				}
				if err = q.Complete(id, assignment.LeaseID, "w", output, errMsg); err != nil {
					t.Fatal(err)
				}
			}
			expected := len("a") + len(request.Model) + len(j.output) + len(j.err)
			if q.total.active != 0 || q.total.jobs != 1 || q.total.bytes != expected || j.req.Prompt != "" || j.req.System != "" || j.req.Format != "" {
				t.Fatalf("terminal retained payload: %+v %+v", q.total, j.req)
			}
			now = now.Add(Retention)
			if _, err = q.Deadline("a", id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("retention: %v", err)
			}
			if q.total != (usage{}) || len(q.callers) != 0 || len(q.jobs) != 0 {
				t.Fatalf("retention leaked: %+v %v", q.total, q.callers)
			}
		})
	}
}

func TestAdmissionRetainedCountAndCallerFirst(t *testing.T) {
	q := boundedQueue(t, Limits{1, 1, 2 << 20, 1, 1, 2 << 20})
	id, err := q.Submit("a", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assignment := claimNow(t, q, "w")
	if err = q.Complete(id, assignment.LeaseID, "w", "", ""); err != nil {
		t.Fatal(err)
	}
	if q.total.active != 0 {
		t.Fatal("active not released")
	}
	if id, err = q.Submit("a", req(time.Minute)); id != "" || !errors.Is(err, ErrCallerCapacity) {
		t.Fatalf("caller first: %q %v", id, err)
	}
	if id, err = q.Submit("b", req(time.Minute)); id != "" || !errors.Is(err, ErrCapacity) {
		t.Fatalf("retained global: %q %v", id, err)
	}
}

func TestCompletionReservedResultAndOversizeKeepsLease(t *testing.T) {
	q := boundedQueue(t, smallLimits())
	id, err := q.Submit("a", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assignment := claimNow(t, q, "w")
	before := q.total
	if err = q.Complete(id, assignment.LeaseID, "w", strings.Repeat("x", MaxResultBytes+1), ""); !errors.Is(err, ErrResultTooLarge) || q.total != before || q.Leases("w") != 1 {
		t.Fatalf("oversize mutated lease: %v %+v", err, q.total)
	}
	if err = q.Complete(id, assignment.LeaseID, "w", strings.Repeat("x", MaxResultBytes), ""); err != nil {
		t.Fatal(err)
	}
	if q.total.bytes > before.bytes || q.total.bytes > q.limits.MaxBytes || q.total.active != 0 {
		t.Fatalf("reserved completion: %+v", q.total)
	}
}

func TestDeadlineCleanupReleasesLeaseAndAdmission(t *testing.T) {
	q := boundedQueue(t, Limits{1, 2, 4 << 20, 1, 2, 4 << 20})
	now := time.Now()
	q.SetClock(func() time.Time { return now })
	_, err := q.Submit("a", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assignment := claimNow(t, q, "w")
	if assignment == nil {
		t.Fatal("no assignment")
	}
	now = now.Add(time.Minute)
	if _, err = q.Submit("a", req(time.Minute)); err != nil {
		t.Fatalf("expired active reservation: %v", err)
	}
	if q.Leases("w") != 0 || q.total.active != 1 || q.total.jobs != 2 {
		t.Fatalf("expiry accounting: %+v", q.total)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, err := q.Wait(ctx, "a", assignment.JobID)
	if err != nil || status.State != protocol.StateExpired {
		t.Fatalf("owner expired: %+v %v", status, err)
	}
}

func TestLimitsValidation(t *testing.T) {
	for _, limits := range []Limits{{MaxBytes: -1}, {MaxActiveJobs: 257}, {CallerMaxJobs: 257}, {CallerMaxBytes: 65 << 20}, {CallerMaxActiveJobs: 65}} {
		if _, err := NewWithLimits(limits); err == nil {
			t.Fatalf("invalid accepted: %+v", limits)
		}
	}
	q := boundedQueue(t, Limits{})
	if q.limits != DefaultLimits() {
		t.Fatalf("defaults: %+v", q.limits)
	}
}

func TestAdmissionConcurrentByteBudget(t *testing.T) {
	request := protocol.SubmitRequest{Model: "m", Prompt: "ü猫", DeadlineMS: 60000}
	unit := requestBytes("a", request.Model, request.Prompt, "", "") + MaxResultBytes
	q := boundedQueue(t, Limits{MaxActiveJobs: 8, MaxJobs: 16, MaxBytes: 2 * unit, CallerMaxActiveJobs: 8, CallerMaxJobs: 16, CallerMaxBytes: 2 * unit})
	var wg sync.WaitGroup
	var accepted int
	var mu sync.Mutex
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := q.Submit("a", request)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			} else if id != "" || !errors.Is(err, ErrCallerCapacity) {
				t.Errorf("byte rejection: %q %v", id, err)
			}
		}()
	}
	wg.Wait()
	if accepted != 2 || q.total.bytes != 2*unit || q.total.active != 2 || q.total.jobs != 2 || len(q.pending) != 2 {
		t.Fatalf("concurrent byte boundary: accepted%d %+v", accepted, q.total)
	}
}

func TestObserveCleansPastRetentionAndExpiryNotification(t *testing.T) {
	q := boundedQueue(t, smallLimits())
	now := time.Now()
	q.SetClock(func() time.Time { return now })
	var notified int
	q.OnExpire = func(id, model, worker string) {
		notified++
		if q.Pending() != 0 {
			t.Error("notification under inconsistent queue")
		}
	}
	if _, err := q.Submit("a", req(time.Minute)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute + Retention)
	observation := q.Observe("", "", 0, 10)
	if observation.Total != 0 || observation.Expired != 1 || q.total != (usage{}) || notified != 1 {
		t.Fatalf("late cleanup: %+v ledger%+v notifications%d", observation, q.total, notified)
	}
}
