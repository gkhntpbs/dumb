package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func TestCancelQueuedNeverDispatches(t *testing.T) {
	q := New()
	id := submitForTest(q, req(time.Second))
	st, err := q.Cancel("caller", id)
	if err != nil || st.State != protocol.StateCancelled || st.JobID != id {
		t.Fatalf("cancel: %+v %v", st, err)
	}
	if q.Pending() != 0 {
		t.Fatal("cancelled job still pending")
	}
	if a := claimNow(t, q, "w1"); a != nil {
		t.Fatalf("cancelled job dispatched: %+v", a)
	}
	st, err = q.Wait(context.Background(), "caller", id)
	if err != nil || st.State != protocol.StateCancelled || st.Output != "" || st.Error != "" {
		t.Fatalf("wait: %+v %v", st, err)
	}
	if prompt := q.jobs[id].req.Prompt; prompt != "" {
		t.Fatalf("prompt retained after cancel: %q", prompt)
	}
}

func TestCancelLeasedEndsLeaseAndRefusesAckAndResult(t *testing.T) {
	q := New()
	id := submitForTest(q, req(time.Second))
	a := claimNow(t, q, "w1")
	if q.Leases("w1") != 1 {
		t.Fatal("no lease")
	}
	if st, err := q.Cancel("caller", id); err != nil || st.State != protocol.StateCancelled {
		t.Fatalf("cancel: %+v %v", st, err)
	}
	if q.Leases("w1") != 0 {
		t.Fatal("lease slot not released")
	}
	if err := q.Ack(id, a.LeaseID, "w1"); err != ErrLease {
		t.Fatalf("ack after cancel: %v", err)
	}
	if err := q.Complete(id, a.LeaseID, "w1", "late", ""); err != ErrLease {
		t.Fatalf("complete after cancel: %v", err)
	}
	if q.Release(id, a.LeaseID) || q.RevokeUnacked(id, a.LeaseID) {
		t.Fatal("cancelled job requeued")
	}
	st, _ := q.Wait(context.Background(), "caller", id)
	if st.State != protocol.StateCancelled || st.Output != "" {
		t.Fatalf("result not discarded: %+v", st)
	}
}

func TestCancelWakesBlockedClaimAtCapacity(t *testing.T) {
	q := New()
	first := submitForTest(q, req(5*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if a, _ := q.Claim(ctx, "w1", models, 1); a == nil || a.JobID != first {
		t.Fatal("first claim")
	}
	second := submitForTest(q, req(5*time.Second))
	got := make(chan *protocol.Assignment, 1)
	go func() {
		a, _ := q.Claim(ctx, "w1", models, 1)
		got <- a
	}()
	time.Sleep(30 * time.Millisecond)
	if _, err := q.Cancel("caller", first); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a == nil || a.JobID != second {
		t.Fatalf("freed slot not reused: %+v", a)
	}
}

func TestCancelRepeatIsIdempotent(t *testing.T) {
	q := New()
	id := submitForTest(q, req(time.Second))
	for i := 0; i < 3; i++ {
		st, err := q.Cancel("caller", id)
		if err != nil || st.State != protocol.StateCancelled {
			t.Fatalf("cancel %d: %+v %v", i, st, err)
		}
	}
	if o := q.Observe("", "", 0, 0); o.Cancelled != 1 {
		t.Fatalf("cancelled counted %d times", o.Cancelled)
	}
}

func TestCancelAfterTerminalKeepsResult(t *testing.T) {
	for _, state := range []string{protocol.StateDone, protocol.StateFailed, protocol.StateExpired} {
		t.Run(state, func(t *testing.T) {
			q := New()
			id := submitForTest(q, req(time.Minute))
			if state == protocol.StateExpired {
				q.SetClock(func() time.Time { return time.Now().Add(70 * time.Second) })
				defer q.SetClock(time.Now)
			} else {
				a := claimNow(t, q, "w1")
				errMsg := ""
				if state == protocol.StateFailed {
					errMsg = "boom"
				}
				out := "answer"
				if errMsg != "" {
					out = ""
				}
				if err := q.Complete(id, a.LeaseID, "w1", out, errMsg); err != nil {
					t.Fatal(err)
				}
			}
			st, err := q.Cancel("caller", id)
			if !errors.Is(err, ErrFinished) || st.State != state {
				t.Fatalf("cancel after %s: %+v %v", state, st, err)
			}
			if state == protocol.StateDone && st.Output != "answer" {
				t.Fatalf("result lost: %+v", st)
			}
		})
	}
}

func TestCancelOwnership(t *testing.T) {
	q := New()
	id := submitForTest(q, req(time.Second))
	for _, owner := range []string{"other", ""} {
		if _, err := q.Cancel(owner, id); err != ErrNotFound {
			t.Fatalf("owner %q: %v", owner, err)
		}
	}
	if _, err := q.Cancel("caller", "missing"); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
	if q.Pending() != 1 {
		t.Fatal("foreign cancel changed the job")
	}
}

func TestCancelVersusDeadline(t *testing.T) {
	// At or after the deadline the job is expired; cancel reports it unchanged.
	q := New()
	id := submitForTest(q, req(time.Minute))
	a := claimNow(t, q, "w1")
	base := time.Now()
	q.SetClock(func() time.Time { return base.Add(time.Minute) })
	st, err := q.Cancel("caller", id)
	if !errors.Is(err, ErrFinished) || st.State != protocol.StateExpired {
		t.Fatalf("cancel at deadline: %+v %v", st, err)
	}
	if err := q.Complete(id, a.LeaseID, "w1", "x", ""); err != ErrLease {
		t.Fatal(err)
	}

	// A cancel committed before the deadline stays cancelled when the timer fires.
	q2 := New()
	id2 := submitForTest(q2, req(30*time.Millisecond))
	if _, err := q2.Cancel("caller", id2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	st, _ = q2.Wait(context.Background(), "caller", id2)
	if st.State != protocol.StateCancelled {
		t.Fatalf("deadline overwrote cancel: %+v", st)
	}
	if o := q2.Observe("", "", 0, 0); o.Expired != 0 || o.Cancelled != 1 {
		t.Fatalf("counters: %+v", o)
	}
}

func TestCancelCompleteRaceHasOneWinner(t *testing.T) {
	for i := 0; i < 200; i++ {
		q := New()
		id := submitForTest(q, req(time.Second))
		a := claimNow(t, q, "w1")
		var wg sync.WaitGroup
		var cancelErr, completeErr error
		var st protocol.JobStatus
		wg.Add(2)
		go func() { defer wg.Done(); st, cancelErr = q.Cancel("caller", id) }()
		go func() { defer wg.Done(); completeErr = q.Complete(id, a.LeaseID, "w1", "out", "") }()
		wg.Wait()
		final, _ := q.Wait(context.Background(), "caller", id)
		switch {
		case cancelErr == nil && completeErr == ErrLease:
			if st.State != protocol.StateCancelled || final.State != protocol.StateCancelled {
				t.Fatalf("cancel won but final %+v", final)
			}
		case errors.Is(cancelErr, ErrFinished) && completeErr == nil:
			if st.State != protocol.StateDone || final.State != protocol.StateDone || final.Output != "out" {
				t.Fatalf("complete won but %+v / %+v", st, final)
			}
		default:
			t.Fatalf("no single winner: cancel=%v complete=%v", cancelErr, completeErr)
		}
		if q.Leases("w1") != 0 {
			t.Fatal("lease leaked")
		}
		q.Close()
	}
}

func TestDurableCancelSurvivesRestart(t *testing.T) {
	for _, leased := range []bool{false, true} {
		directory := privateStateDirectory(t)
		q := durableQueue(t, directory, time.Now)
		id, err := q.SubmitKey("a", "cancel-key", req(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		var a *protocol.Assignment
		if leased {
			if a, err = q.ClaimWithAck(context.Background(), "worker", models, 1, time.Second); err != nil || a == nil {
				t.Fatal(err)
			}
		}
		if _, err = q.Cancel("a", id); err != nil {
			t.Fatal(err)
		}
		retain := q.jobs[id].retainUntil
		if err = q.Close(); err != nil {
			t.Fatal(err)
		}
		r := durableQueue(t, directory, time.Now)
		st, err := r.Wait(context.Background(), "a", id)
		if err != nil || st.State != protocol.StateCancelled {
			t.Fatalf("after restart: %+v %v", st, err)
		}
		if !r.jobs[id].retainUntil.Equal(retain) || r.jobs[id].req.Prompt != "" {
			t.Fatal("retention or prompt changed by restart")
		}
		if replay, found, err := r.Replay("a", "cancel-key", req(time.Minute)); err != nil || !found || replay != id {
			t.Fatalf("key replay: %v %v %v", replay, found, err)
		}
		if st, err := r.Cancel("a", id); err != nil || st.State != protocol.StateCancelled {
			t.Fatalf("repeat after restart: %+v %v", st, err)
		}
		if leased {
			if err := r.Complete(id, a.LeaseID, "worker", "late", ""); err != ErrLease {
				t.Fatalf("result after restart: %v", err)
			}
			if r.Leases("worker") != 0 {
				t.Fatal("cancelled lease restored")
			}
		}
		if got := claimNowCapacity(t, r, "worker"); got != nil {
			t.Fatal("cancelled job dispatched after restart")
		}
	}
}

func TestDurableCancelWriteFailureIsNotAcknowledged(t *testing.T) {
	directory := privateStateDirectory(t)
	q := durableQueue(t, directory, time.Now)
	id, err := q.Submit("a", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	q.store.rename = func(string, string) error { return errors.New("injected") }
	if _, err := q.Cancel("a", id); !errors.Is(err, ErrStorage) {
		t.Fatalf("cancel with failing storage: %v", err)
	}
	if q.jobs[id].state != protocol.StateQueued {
		t.Fatal("unpersisted cancel was published")
	}
}

func claimNowCapacity(t *testing.T, q *Queue, worker string) *protocol.Assignment {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	a, _ := q.Claim(ctx, worker, models, 1)
	return a
}
