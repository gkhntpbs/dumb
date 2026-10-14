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
	limits    Limits
	total     usage
	callers   map[string]*usage
	reports   []expiration
	completed int
	failed    int
	expired   int
	samples   []measurement
	mu        sync.Mutex
	jobs      map[string]*job
	pending   []*job         // FIFO of queued jobs
	leases    map[string]int // live leases per worker
	wake      chan struct{}  // closed and replaced whenever a job is queued or a lease ends

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
	limits, err := limits.Normalize()
	if err != nil {
		return nil, err
	}
	return &Queue{limits: limits, callers: map[string]*usage{}, now: time.Now, jobs: map[string]*job{}, leases: map[string]int{}, wake: make(chan struct{})}, nil
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
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
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
	j := &job{owner: owner, bytes: bytes, created: now, req: req, id: newID(), deadline: now.Add(ttl), state: protocol.StateQueued, done: make(chan struct{})}
	q.jobs[j.id] = j
	q.pending = append(q.pending, j)
	q.callers[owner] = u
	u.active++
	u.jobs++
	u.bytes += bytes
	q.total.active++
	q.total.jobs++
	q.total.bytes += bytes
	j.deadlineTimer = time.AfterFunc(ttl, func() { q.expire(j) })
	q.wakeLocked()
	return j.id, nil
}

// Claim blocks until a queued job for one of models is available and workerID
// holds fewer than capacity live leases, then leases the job to workerID. It
// returns nil, nil when ctx ends first.
func (q *Queue) Claim(ctx context.Context, workerID string, models []string, capacity int) (*protocol.Assignment, error) {
	defer q.flushExpirations()
	for {
		q.mu.Lock()
		q.cleanupLocked()
		now := q.now()
		for i, j := range q.pending {
			if q.leases[workerID] >= capacity {
				break
			}
			if !slices.Contains(models, j.req.Model) || !now.Before(j.deadline) {
				continue
			}
			q.pending = slices.Delete(q.pending, i, i+1)
			j.leased = now
			j.state = protocol.StateLeased
			j.leaseID = newID()
			j.workerID = workerID
			j.acked = false
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
	j, ok := q.jobs[jobID]
	if !ok || j.state != protocol.StateLeased || j.leaseID != leaseID || j.workerID != workerID || !q.now().Before(j.deadline) {
		return ErrLease
	}
	if len(output) > MaxResultBytes-len(errMsg) {
		return ErrResultTooLarge
	}
	q.endLeaseLocked(j)
	if errMsg != "" {
		q.failed++
		j.state, j.err = protocol.StateFailed, errMsg
	} else {
		q.completed++
		j.state, j.output = protocol.StateDone, output
	}
	q.recordLocked(j, q.now())
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
	j, ok := q.jobs[jobID]
	if !ok || j.state != protocol.StateLeased || j.leaseID != leaseID || j.workerID != workerID || !q.now().Before(j.deadline) {
		return ErrLease
	}
	j.acked = true
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
	q.endLeaseLocked(j)
	j.leased = time.Time{}
	j.state, j.leaseID, j.workerID = protocol.StateQueued, "", ""
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
	j, ok := q.jobs[jobID]
	q.mu.Unlock()
	q.flushExpirations()
	if !ok || j.owner != owner {
		return protocol.JobStatus{}, ErrNotFound
	}
	select {
	case <-j.done:
	case <-ctx.Done():
	}
	q.mu.Lock()
	q.cleanupLocked()
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
	q.expireLocked(j)
	q.mu.Unlock()
	q.notify(j)
}

// expireLocked marks an unfinished job expired. The OnExpire call is deferred
// to notify, which runs once per job outside the lock.
func (q *Queue) expireLocked(j *job) {
	switch j.state {
	case protocol.StateQueued:
		if i := slices.Index(q.pending, j); i >= 0 {
			q.pending = slices.Delete(q.pending, i, i+1)
		}
	case protocol.StateLeased:
		q.endLeaseLocked(j)
	default:
		return
	}
	q.expired++
	j.state = protocol.StateExpired
	j.expireUnreported = true
	q.finishLocked(j)
}

// notify calls OnExpire once for a job expireLocked has just expired.
func (q *Queue) notify(j *job) { q.flushExpirations() }

func (q *Queue) finishLocked(j *job) {
	if j.deadlineTimer != nil {
		j.deadlineTimer.Stop()
	}
	if j.expireUnreported {
		q.reports = append(q.reports, expiration{j.id, j.req.Model, j.workerID})
		j.expireUnreported = false
	}
	oldBytes := j.bytes
	j.req.Prompt = ""
	j.req.System = ""
	j.req.Format = ""
	j.req.NumPredict = 0
	j.bytes = len(j.owner) + len(j.req.Model) + len(j.output) + len(j.err)
	q.total.active--
	q.total.bytes -= oldBytes - j.bytes
	u := q.callers[j.owner]
	u.active--
	u.bytes -= oldBytes - j.bytes
	close(j.done)
	finished := q.now()
	if j.state == protocol.StateExpired {
		finished = j.deadline
	}
	j.retainUntil = finished.Add(Retention)
	j.retentionTimer = time.AfterFunc(max(j.retainUntil.Sub(q.now()), 0), func() { q.mu.Lock(); q.removeLocked(j); q.mu.Unlock() })
}
