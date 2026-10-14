// Package queue is the server's in-memory job queue.
//
// A job lives until its deadline. When a worker claims it, the job is leased to
// that worker until the same deadline. A job whose deadline passes — queued or
// leased — becomes expired and is never handed out again: the caller has already
// taken its fallback path.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

var (
	ErrNotFound       = errors.New("job not found")
	ErrCallerCapacity = errors.New("caller queue capacity exhausted")
	ErrCapacity       = errors.New("queue capacity exhausted")
	ErrResultTooLarge = errors.New("result payload exceeds maximum bytes")
	ErrOwner          = errors.New("caller identity is required")
	// ErrLease means the lease is unknown, expired, already used, or held by another worker.
	ErrLease = errors.New("lease is not valid")
)

// Retention is how long a finished job stays readable after it finishes.
const Retention = time.Minute

type job struct {
	diskCharge     int64
	key            string
	digest         [32]byte
	order          int64
	ackDue         time.Time
	ackTimer       *time.Timer
	bytes          int
	retainUntil    time.Time
	deadlineTimer  *time.Timer
	retentionTimer *time.Timer
	owner          string
	created        time.Time
	leased         time.Time
	req            protocol.SubmitRequest
	id             string
	deadline       time.Time
	state          string
	leaseID        string
	workerID       string
	output         string
	err            string
	acked          bool          // the lease holder acknowledged the assignment
	done           chan struct{} // closed when state becomes done, failed or expired

	expireUnreported bool // expired, OnExpire not called yet
}

type Queue struct {
	store           *stateStore
	keys            map[retryKey]*job
	stop            chan struct{}
	closed, halted  bool
	sequence, front int64
	limits          Limits
	total           usage
	callers         map[string]*usage
	reports         []expiration
	completed       int
	failed          int
	expired         int
	samples         []measurement
	mu              sync.Mutex
	jobs            map[string]*job
	pending         []*job         // FIFO of queued jobs
	leases          map[string]int // live leases per worker
	wake            chan struct{}  // closed and replaced whenever a job is queued or a lease ends

	// OnExpire, if set, is called (without the lock held) when a job's
	// deadline passes before it finished; workerID is "" for a queued job.
	OnExpire func(jobID, model, workerID string)

	now func() time.Time // clock for deadlines, waits and observations; time.Now unless SetClock
}

func New() *Queue {
	q, _ := NewWithLimits(Limits{})
	return q
}

func NewWithLimits(limits Limits) (*Queue, error) {
	return Open(limits, StoreConfig{}, time.Now)
}

// SetClock replaces the clock used for deadlines, wait times and observations.
// Call it before the queue is used; deadline timers still run on real time.
func (q *Queue) SetClock(now func() time.Time) { q.now = now }

// wakeLocked wakes every blocked Claim.
func (q *Queue) wakeLocked() {
	close(q.wake)
	q.wake = make(chan struct{})
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Submit queues a job that must finish within its DeadlineMS and returns its ID.
func (q *Queue) Submit(owner string, req protocol.SubmitRequest) (string, error) {
	return q.SubmitKey(owner, "", req)
}

func (q *Queue) SubmitKey(owner, key string, req protocol.SubmitRequest) (string, error) {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	if id, found, err := q.replayLocked(owner, key, req); found || err != nil {
		return id, err
	}
	if owner == "" {
		return "", ErrOwner
	}
	bytes, fits := boundedRequestBytes(q.limits.CallerMaxBytes-MaxResultBytes, owner, req.Model, req.Prompt, req.System, req.Format)
	if !fits {
		return "", ErrCallerCapacity
	}
	bytes += MaxResultBytes
	u := q.callers[owner]
	if u == nil {
		u = &usage{}
	}
	if u.active >= q.limits.CallerMaxActiveJobs || u.jobs >= q.limits.CallerMaxJobs || bytes > q.limits.CallerMaxBytes-u.bytes {
		return "", ErrCallerCapacity
	}
	if q.total.active >= q.limits.MaxActiveJobs || q.total.jobs >= q.limits.MaxJobs || bytes > q.limits.MaxBytes-q.total.bytes {
		return "", ErrCapacity
	}
	ttl := time.Duration(req.DeadlineMS) * time.Millisecond
	now := q.now()
	j := &job{key: key, digest: payloadDigest(req), order: q.sequence + 1, owner: owner, bytes: bytes, created: now, req: req, id: newID(), deadline: now.Add(ttl), state: protocol.StateQueued, done: make(chan struct{})}
	if err := q.persistLocked(j); err != nil {
		return "", err
	}
	q.sequence++
	if key != "" {
		q.keys[retryKey{owner, key}] = j
	}
	q.jobs[j.id] = j
	q.pending = append(q.pending, j)
	q.callers[owner] = u
	u.active++
	u.jobs++
	u.bytes += bytes
	q.total.active++
	q.total.jobs++
	q.total.bytes += bytes
	q.scheduleDeadlineLocked(j)
	q.wakeLocked()
	return j.id, nil
}

// Claim blocks until a queued job for one of models is available and workerID
// holds fewer than capacity live leases, then leases the job to workerID. It
// returns nil, nil when ctx ends first.
func (q *Queue) Claim(ctx context.Context, workerID string, models []string, capacity int) (*protocol.Assignment, error) {
	return q.ClaimWithAck(ctx, workerID, models, capacity, 0)
}

func (q *Queue) ClaimWithAck(ctx context.Context, workerID string, models []string, capacity int, ackWindow time.Duration) (*protocol.Assignment, error) {
	defer q.flushExpirations()
	for {
		q.mu.Lock()
		q.cleanupLocked()
		if err := q.storageErrorLocked(); err != nil {
			q.mu.Unlock()
			return nil, err
		}
		now := q.now()
		for i, j := range q.pending {
			if q.leases[workerID] >= capacity {
				break
			}
			if !slices.Contains(models, j.req.Model) || !now.Before(j.deadline) {
				continue
			}
			next := *j
			next.leased = now
			next.state = protocol.StateLeased
			next.leaseID = newID()
			next.workerID = workerID
			next.acked = false
			next.ackDue = time.Time{}
			if ackWindow > 0 {
				next.ackDue = minTime(now.Add(ackWindow), j.deadline)
			}
			if err := q.persistLocked(&next); err != nil {
				q.mu.Unlock()
				return nil, err
			}
			q.pending = slices.Delete(q.pending, i, i+1)
			*j = next
			q.scheduleAckLocked(j)
			q.leases[workerID]++
			a := &protocol.Assignment{
				JobID:          j.id,
				LeaseID:        j.leaseID,
				LeaseExpiresAt: j.deadline,
				Model:          j.req.Model,
				Prompt:         j.req.Prompt,
				System:         j.req.System,
				Format:         j.req.Format,
				NumPredict:     j.req.NumPredict,
			}
			q.mu.Unlock()
			return a, nil
		}
		wake := q.wake
		q.mu.Unlock()
		q.flushExpirations()
		select {
		case <-q.stop:
			return nil, ErrStorage
		case <-ctx.Done():
			return nil, nil
		case <-wake:
		}
	}
}

// Complete records the result of a leased job. Only the worker holding the
// live lease may complete it; errMsg != "" marks the job failed.
func (q *Queue) Complete(jobID, leaseID, workerID, output, errMsg string) error {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	if err := q.storageErrorLocked(); err != nil {
		return err
	}
	now := q.now()
	j, ok := q.jobs[jobID]
	if !ok || j.state != protocol.StateLeased || j.leaseID != leaseID || j.workerID != workerID || !now.Before(j.deadline) {
		return ErrLease
	}
	if len(output) > MaxResultBytes-len(errMsg) {
		return ErrResultTooLarge
	}
	next := *j
	if errMsg != "" {
		next.state, next.err = protocol.StateFailed, errMsg
	} else {
		next.state, next.output = protocol.StateDone, output
	}
	next = q.prepareTerminalAt(&next, now)
	if err := q.persistLocked(&next); err != nil {
		return err
	}
	q.endLeaseLocked(j)
	oldBytes := j.bytes
	*j = next
	j.bytes = oldBytes
	if errMsg != "" {
		q.failed++
	} else {
		q.completed++
	}
	q.recordLocked(j, now)
	q.finishLocked(j)
	return nil
}

// Ack records that the worker holding the live lease received the
// assignment. Acking twice is fine; any other lease gets ErrLease.
func (q *Queue) Ack(jobID, leaseID, workerID string) error {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	if err := q.storageErrorLocked(); err != nil {
		return err
	}
	j, ok := q.jobs[jobID]
	if !ok || j.state != protocol.StateLeased || j.leaseID != leaseID || j.workerID != workerID || !q.now().Before(j.deadline) {
		return ErrLease
	}
	if j.acked {
		return nil
	}
	next := *j
	next.acked = true
	if err := q.persistLocked(&next); err != nil {
		return err
	}
	*j = next
	q.scheduleAckLocked(j)
	return nil
}

// RevokeUnacked is Release for a lease that was never acknowledged: it
// requeues the job at the front unless Ack (or a result) came first.
func (q *Queue) RevokeUnacked(jobID, leaseID string) bool {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	j, ok := q.jobs[jobID]
	if !ok || j.acked {
		return false
	}
	return q.releaseLocked(j, leaseID)
}

// Release puts a leased job back at the front of the queue, as if it had never
// been claimed. The server calls it when the assignment could not be delivered
// (the long-poll was aborted in the instant the job was claimed). It reports
// whether the job was requeued; a job past its deadline is left to expire.
func (q *Queue) Release(jobID, leaseID string) bool {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	j, ok := q.jobs[jobID]
	return ok && q.releaseLocked(j, leaseID)
}

func (q *Queue) releaseLocked(j *job, leaseID string) bool {
	if j.state != protocol.StateLeased || j.leaseID != leaseID || !q.now().Before(j.deadline) {
		return false
	}
	next := *j
	next.leased = time.Time{}
	next.state, next.leaseID, next.workerID = protocol.StateQueued, "", ""
	next.acked = false
	next.ackDue = time.Time{}
	next.order = q.front
	if err := q.persistLocked(&next); err != nil {
		return false
	}
	q.front--
	q.endLeaseLocked(j)
	*j = next
	q.scheduleAckLocked(j)
	q.pending = slices.Insert(q.pending, 0, j)
	q.wakeLocked()
	return true
}

// Leases returns the number of live leases workerID holds.
func (q *Queue) Leases(workerID string) int {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	return q.leases[workerID]
}

// endLeaseLocked frees the lease slot of a leased job and wakes waiting claims.
func (q *Queue) endLeaseLocked(j *job) {
	if q.leases[j.workerID]--; q.leases[j.workerID] <= 0 {
		delete(q.leases, j.workerID)
	}
	q.wakeLocked()
}

// Wait blocks until the job finishes or ctx ends, then returns its status.
func (q *Queue) Wait(ctx context.Context, owner, jobID string) (protocol.JobStatus, error) {
	defer q.flushExpirations()
	q.mu.Lock()
	q.cleanupLocked()
	if err := q.storageErrorLocked(); err != nil {
		q.mu.Unlock()
		return protocol.JobStatus{}, err
	}
	j, ok := q.jobs[jobID]
	owned := ok && j.owner == owner
	var done <-chan struct{}
	if owned {
		done = j.done
	}
	q.mu.Unlock()
	q.flushExpirations()
	if !owned {
		return protocol.JobStatus{}, ErrNotFound
	}
	select {
	case <-q.stop:
		return protocol.JobStatus{}, ErrStorage
	case <-done:
	case <-ctx.Done():
	}
	q.mu.Lock()
	q.cleanupLocked()
	if err := q.storageErrorLocked(); err != nil {
		q.mu.Unlock()
		return protocol.JobStatus{}, err
	}
	if q.jobs[j.id] != j {
		q.mu.Unlock()
		return protocol.JobStatus{}, ErrNotFound
	}
	// The caller's wait is usually clamped to the deadline, so it can end a hair
	// before the expiry timer fires; a past deadline is expired either way.
	if !q.now().Before(j.deadline) {
		q.expireLocked(j)
	}
	st := protocol.JobStatus{JobID: j.id, State: j.state, Output: j.output, Error: j.err}
	q.mu.Unlock()
	q.notify(j)
	return st, nil
}

// Deadline returns the job's deadline.
func (q *Queue) Deadline(owner, jobID string) (time.Time, error) {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	if err := q.storageErrorLocked(); err != nil {
		return time.Time{}, err
	}
	j, ok := q.jobs[jobID]
	if !ok || j.owner != owner {
		return time.Time{}, ErrNotFound
	}
	return j.deadline, nil
}

// Pending returns the number of queued jobs.
func (q *Queue) Pending() int {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	return len(q.pending)
}

func (q *Queue) expire(j *job) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.expireLocked(j)
	q.mu.Unlock()
	q.notify(j)
}

// expireLocked marks an unfinished job expired. The OnExpire call is deferred
// to notify, which runs once per job outside the lock.
func (q *Queue) expireLocked(j *job) {
	if !active(j) || q.closed {
		return
	}
	next := *j
	next.state = protocol.StateExpired
	next.expireUnreported = true
	next = q.prepareTerminal(&next)
	if err := q.persistLocked(&next); err != nil {
		return
	}
	if j.state == protocol.StateQueued {
		if i := slices.Index(q.pending, j); i >= 0 {
			q.pending = slices.Delete(q.pending, i, i+1)
		}
	} else {
		q.endLeaseLocked(j)
	}
	oldBytes := j.bytes
	*j = next
	j.bytes = oldBytes
	q.expired++
	q.finishLocked(j)
}

// notify calls OnExpire once for a job expireLocked has just expired.
func (q *Queue) notify(j *job) { q.flushExpirations() }

func (q *Queue) finishLocked(j *job) {
	if j.deadlineTimer != nil {
		j.deadlineTimer.Stop()
	}
	if j.ackTimer != nil {
		j.ackTimer.Stop()
		j.ackTimer = nil
	}
	if j.expireUnreported {
		q.reports = append(q.reports, expiration{j.id, j.req.Model, j.workerID})
		j.expireUnreported = false
	}
	oldBytes := j.bytes
	next := q.prepareTerminal(j)
	*j = next
	q.total.active--
	q.total.bytes -= oldBytes - j.bytes
	u := q.callers[j.owner]
	u.active--
	u.bytes -= oldBytes - j.bytes
	q.store.releaseReservation(j.id)
	close(j.done)
	q.scheduleRetentionLocked(j)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// StorageError reports whether further mutations must fail closed. Callers use
// it to distinguish a lease already expired from an undelivered release whose
// durable write failed; it never exposes paths or underlying IO details.
func (q *Queue) StorageError() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.storageErrorLocked()
}
