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
	a, err := q.Claim(ctx, worker, models, 100)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestClaimComplete(t *testing.T) {
	q := New()
	id := submitForTest(q, req(time.Second))
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
	st, _ := q.Wait(context.Background(), "caller", id)
	if st.State != protocol.StateDone || st.Output != "out" {
		t.Fatalf("status: %+v", st)
	}
	if err := q.Complete(id, a.LeaseID, "w1", "again", ""); err != ErrLease {
		t.Errorf("second complete: got %v, want ErrLease", err)
	}
}

func TestCompleteFailed(t *testing.T) {
	q := New()
	id := submitForTest(q, req(time.Second))
	a := claimNow(t, q, "w1")
	q.Complete(id, a.LeaseID, "w1", "", "model crashed")
	st, _ := q.Wait(context.Background(), "caller", id)
	if st.State != protocol.StateFailed || st.Error != "model crashed" {
		t.Fatalf("status: %+v", st)
	}
}

func TestCompleteRequiresLeaseHolder(t *testing.T) {
	q := New()
	id := submitForTest(q, req(time.Second))
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
	submitForTest(q, protocol.SubmitRequest{Model: "other", Prompt: "x", DeadlineMS: 1000})
	if a := claimNow(t, q, "w1"); a != nil {
		t.Fatalf("got job for a model the worker lacks: %+v", a)
	}
}

func TestExpiredLeaseNotRedispatched(t *testing.T) {
	q := New()
	id := submitForTest(q, req(40*time.Millisecond))
	a := claimNow(t, q, "w1")
	time.Sleep(80 * time.Millisecond)

	if err := q.Complete(id, a.LeaseID, "w1", "late", ""); err != ErrLease {
		t.Errorf("late complete: got %v, want ErrLease", err)
	}
	st, _ := q.Wait(context.Background(), "caller", id)
	if st.State != protocol.StateExpired {
		t.Errorf("state: got %s, want expired", st.State)
	}
	if got := claimNow(t, q, "w2"); got != nil {
		t.Fatalf("expired job re-dispatched: %+v", got)
	}
}

func TestExpiredWhileQueuedNotDispatched(t *testing.T) {
	q := New()
	submitForTest(q, req(20*time.Millisecond))
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
	id := submitForTest(q, req(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	st, err := q.Wait(ctx, "caller", id)
	if err != nil || st.State != protocol.StateQueued {
		t.Fatalf("got %+v, %v", st, err)
	}
	if _, err := q.Wait(ctx, "caller", "nope"); err != ErrNotFound {
		t.Errorf("unknown: got %v", err)
	}
}

func TestClaimWakesOnSubmit(t *testing.T) {
	q := New()
	got := make(chan time.Time, 1)
	go func() {
		q.Claim(context.Background(), "w1", models, 100)
		got <- time.Now()
	}()
	time.Sleep(20 * time.Millisecond) // let the claimer block
	start := time.Now()
	submitForTest(q, req(time.Second))
	select {
	case at := <-got:
		if d := at.Sub(start); d > 50*time.Millisecond {
			t.Fatalf("added latency %v, want < 50ms", d)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked claimer never woke")
	}
}

// TestWaitAtDeadlineReportsExpired covers a caller whose wait ends exactly at the
// deadline, racing the expiry timer: it must see expired, never queued.
func TestWaitAtDeadlineReportsExpired(t *testing.T) {
	for range 20 {
		q := New()
		id := submitForTest(q, protocol.SubmitRequest{Model: "m", Prompt: "p", DeadlineMS: 5})
		d, _ := q.Deadline("caller", id)
		ctx, cancel := context.WithDeadline(context.Background(), d)
		st, err := q.Wait(ctx, "caller", id)
		cancel()
		if err != nil || st.State != protocol.StateExpired {
			t.Fatalf("got %+v, %v; want expired", st, err)
		}
	}
}

func claimCap(t *testing.T, q *Queue, worker string, capacity int, wait time.Duration) *protocol.Assignment {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	a, err := q.Claim(ctx, worker, models, capacity)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestCapacity: a worker never holds more live leases than its capacity, and a
// lease frees its slot on complete, fail and expiry.
func TestCapacity(t *testing.T) {
	q := New()
	for range 4 {
		submitForTest(q, req(time.Second))
	}
	a := claimCap(t, q, "w1", 2, 20*time.Millisecond)
	b := claimCap(t, q, "w1", 2, 20*time.Millisecond)
	if a == nil || b == nil || q.Leases("w1") != 2 {
		t.Fatalf("first two claims: %v %v, leases %d", a, b, q.Leases("w1"))
	}
	if got := claimCap(t, q, "w1", 2, 30*time.Millisecond); got != nil {
		t.Fatalf("third claim beyond capacity 2 got %+v", got)
	}
	if got := claimCap(t, q, "w2", 1, 20*time.Millisecond); got == nil {
		t.Fatal("another worker's capacity is independent")
	}

	// A claim blocked on capacity proceeds the moment a lease ends (complete).
	got := make(chan time.Time, 1)
	go func() {
		if claimCap(t, q, "w1", 2, time.Second) != nil {
			got <- time.Now()
		}
	}()
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	if err := q.Complete(a.JobID, a.LeaseID, "w1", "ok", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case at := <-got:
		if d := at.Sub(start); d > 50*time.Millisecond {
			t.Fatalf("capacity wait added %v, want < 50ms", d)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked claim never woke after complete")
	}
	// Failing frees a slot too.
	if err := q.Complete(b.JobID, b.LeaseID, "w1", "", "boom"); err != nil {
		t.Fatal(err)
	}
	if q.Leases("w1") != 1 {
		t.Fatalf("leases after complete+fail: %d, want 1", q.Leases("w1"))
	}
}

func TestCapacityFreesOnExpiry(t *testing.T) {
	q := New()
	expired := make(chan string, 2)
	q.OnExpire = func(id, _, w string) { expired <- id + "@" + w }
	id := submitForTest(q, req(40*time.Millisecond))
	submitForTest(q, req(time.Second))
	if a := claimCap(t, q, "w1", 1, 20*time.Millisecond); a == nil || a.JobID != id {
		t.Fatalf("claim: %+v", a)
	}
	// Blocked on capacity until the first lease expires.
	if a := claimCap(t, q, "w1", 1, time.Second); a == nil {
		t.Fatal("expired lease never freed its slot")
	}
	if got := <-expired; got != id+"@w1" {
		t.Fatalf("OnExpire: %s, want %s@w1", got, id)
	}
}

// TestReleaseRequeuesAtFront: an assignment that could not be delivered goes
// back to the front of the queue, keeps its deadline and frees the lease.
func TestReleaseRequeuesAtFront(t *testing.T) {
	q := New()
	first := submitForTest(q, req(time.Second))
	submitForTest(q, req(time.Second))
	a := claimCap(t, q, "w1", 1, 20*time.Millisecond)
	if a.JobID != first {
		t.Fatal("FIFO broken")
	}
	if !q.Release(a.JobID, a.LeaseID) || q.Leases("w1") != 0 || q.Pending() != 2 {
		t.Fatalf("release: leases %d pending %d", q.Leases("w1"), q.Pending())
	}
	if q.Release(a.JobID, a.LeaseID) {
		t.Fatal("double release accepted")
	}
	if err := q.Complete(a.JobID, a.LeaseID, "w1", "x", ""); err != ErrLease {
		t.Fatalf("stale lease completed: %v", err)
	}
	b := claimCap(t, q, "w2", 1, 20*time.Millisecond)
	if b == nil || b.JobID != first || b.LeaseID == a.LeaseID || !b.LeaseExpiresAt.Equal(a.LeaseExpiresAt) {
		t.Fatalf("requeued job: %+v (was %+v)", b, a)
	}
	// Past its deadline a job is not requeued.
	q2 := New()
	submitForTest(q2, req(30*time.Millisecond))
	c := claimCap(t, q2, "w1", 1, 20*time.Millisecond)
	time.Sleep(40 * time.Millisecond)
	if q2.Release(c.JobID, c.LeaseID) || q2.Pending() != 0 {
		t.Fatal("expired job requeued")
	}
}

// TestAckBlocksRevoke: an acknowledged lease is never revoked; an
// unacknowledged one goes back to the front and its old lease is dead.
func TestAckBlocksRevoke(t *testing.T) {
	q := New()
	submitForTest(q, req(time.Second))
	a := claimCap(t, q, "w1", 1, 20*time.Millisecond)
	if err := q.Ack(a.JobID, a.LeaseID, "w2"); err != ErrLease {
		t.Fatalf("ack by another worker: %v", err)
	}
	if err := q.Ack(a.JobID, a.LeaseID, "w1"); err != nil {
		t.Fatal(err)
	}
	if q.RevokeUnacked(a.JobID, a.LeaseID) || q.Leases("w1") != 1 {
		t.Fatal("acked lease revoked")
	}

	id := submitForTest(q, req(time.Second))
	b := claimCap(t, q, "w2", 1, 20*time.Millisecond)
	if b.JobID != id {
		t.Fatal("wrong job")
	}
	if !q.RevokeUnacked(b.JobID, b.LeaseID) || q.Leases("w2") != 0 || q.Pending() != 1 {
		t.Fatalf("unacked lease not revoked: leases %d pending %d", q.Leases("w2"), q.Pending())
	}
	if q.Ack(b.JobID, b.LeaseID, "w2") != ErrLease || q.Complete(b.JobID, b.LeaseID, "w2", "x", "") != ErrLease {
		t.Fatal("revoked lease still usable")
	}
	c := claimCap(t, q, "w3", 1, 20*time.Millisecond)
	if c == nil || c.JobID != id {
		t.Fatalf("revoked job not claimable again: %+v", c)
	}
	// An acked lease that is released (undelivered) does not pass its ack on.
	if q.Ack(c.JobID, c.LeaseID, "w3") != nil || !q.Release(c.JobID, c.LeaseID) {
		t.Fatal("ack + release failed")
	}
	d := claimCap(t, q, "w4", 1, 20*time.Millisecond)
	if d == nil || !q.RevokeUnacked(d.JobID, d.LeaseID) {
		t.Fatal("ack carried over to the next lease")
	}
}

func submitForTest(q *Queue, req protocol.SubmitRequest) string {
	id, err := q.Submit("caller", req)
	if err != nil {
		panic(err)
	}
	return id
}
