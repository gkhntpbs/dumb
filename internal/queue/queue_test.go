package queue

import (
	"context"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

var models = []string{"llama3.1:8b"}

func req(deadline time.Duration) protocol.SubmitRequest {
	return protocol.SubmitRequest{Model: models[0], Prompt: "hi", DeadlineMS: int(deadline / time.Millisecond)}
}

func claimNow(t *testing.T, q *Queue, worker string) *protocol.Assignment {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	a, err := q.Claim(ctx, worker, models)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestClaimComplete(t *testing.T) {
	q := New()
	id := q.Submit(req(time.Second))
	a := claimNow(t, q, "w1")
	if a == nil || a.JobID != id || a.LeaseID == "" {
		t.Fatalf("claim: got %+v", a)
	}
	if q.Pending() != 0 {
		t.Fatal("claimed job still pending")
	}
	if got := claimNow(t, q, "w2"); got != nil {
		t.Fatalf("leased job dispatched twice: %+v", got)
	}
	if err := q.Complete(id, a.LeaseID, "w1", "out", ""); err != nil {
		t.Fatal(err)
	}
	st, _ := q.Wait(context.Background(), id)
	if st.State != protocol.StateDone || st.Output != "out" {
		t.Fatalf("status: %+v", st)
	}
	if err := q.Complete(id, a.LeaseID, "w1", "again", ""); err != ErrLease {
		t.Errorf("second complete: got %v, want ErrLease", err)
	}
}

func TestCompleteFailed(t *testing.T) {
	q := New()
	id := q.Submit(req(time.Second))
	a := claimNow(t, q, "w1")
	q.Complete(id, a.LeaseID, "w1", "", "model crashed")
	st, _ := q.Wait(context.Background(), id)
	if st.State != protocol.StateFailed || st.Error != "model crashed" {
		t.Fatalf("status: %+v", st)
	}
}

func TestCompleteRequiresLeaseHolder(t *testing.T) {
	q := New()
	id := q.Submit(req(time.Second))
	a := claimNow(t, q, "w1")
	if err := q.Complete(id, a.LeaseID, "w2", "x", ""); err != ErrLease {
		t.Errorf("other worker: got %v", err)
	}
	if err := q.Complete(id, "forged", "w1", "x", ""); err != ErrLease {
		t.Errorf("wrong lease: got %v", err)
	}
	if err := q.Complete("nope", a.LeaseID, "w1", "x", ""); err != ErrLease {
		t.Errorf("unknown job: got %v", err)
	}
}

func TestClaimSkipsOtherModels(t *testing.T) {
	q := New()
	q.Submit(protocol.SubmitRequest{Model: "other", Prompt: "x", DeadlineMS: 1000})
	if a := claimNow(t, q, "w1"); a != nil {
		t.Fatalf("got job for a model the worker lacks: %+v", a)
	}
}

func TestExpiredLeaseNotRedispatched(t *testing.T) {
	q := New()
	id := q.Submit(req(40 * time.Millisecond))
	a := claimNow(t, q, "w1")
	time.Sleep(80 * time.Millisecond)

	if err := q.Complete(id, a.LeaseID, "w1", "late", ""); err != ErrLease {
		t.Errorf("late complete: got %v, want ErrLease", err)
	}
	st, _ := q.Wait(context.Background(), id)
	if st.State != protocol.StateExpired {
		t.Errorf("state: got %s, want expired", st.State)
	}
	if got := claimNow(t, q, "w2"); got != nil {
		t.Fatalf("expired job re-dispatched: %+v", got)
	}
}

func TestExpiredWhileQueuedNotDispatched(t *testing.T) {
	q := New()
	q.Submit(req(20 * time.Millisecond))
	time.Sleep(50 * time.Millisecond)
	if q.Pending() != 0 {
		t.Fatal("expired job still pending")
	}
	if got := claimNow(t, q, "w1"); got != nil {
		t.Fatalf("expired job dispatched: %+v", got)
	}
}

func TestWaitEndsAtContext(t *testing.T) {
	q := New()
	id := q.Submit(req(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	st, err := q.Wait(ctx, id)
	if err != nil || st.State != protocol.StateQueued {
		t.Fatalf("got %+v, %v", st, err)
	}
	if _, err := q.Wait(ctx, "nope"); err != ErrNotFound {
		t.Errorf("unknown: got %v", err)
	}
}

func TestClaimWakesOnSubmit(t *testing.T) {
	q := New()
	got := make(chan time.Time, 1)
	go func() {
		q.Claim(context.Background(), "w1", models)
		got <- time.Now()
	}()
	time.Sleep(20 * time.Millisecond) // let the claimer block
	start := time.Now()
	q.Submit(req(time.Second))
	select {
	case at := <-got:
		if d := at.Sub(start); d > 50*time.Millisecond {
			t.Fatalf("added latency %v, want < 50ms", d)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked claimer never woke")
	}
}
