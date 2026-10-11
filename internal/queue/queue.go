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
	ErrNotFound = errors.New("job not found")
	// ErrLease means the lease is unknown, expired, already used, or held by another worker.
	ErrLease = errors.New("lease is not valid")
)

// Retention is how long a finished job stays readable after it finishes.
const Retention = time.Minute

type job struct {
	req      protocol.SubmitRequest
	id       string
	deadline time.Time
	state    string
	leaseID  string
	workerID string
	output   string
	err      string
	acked    bool          // the lease holder acknowledged the assignment
	done     chan struct{} // closed when state becomes done, failed or expired

	expireUnreported bool // expired, OnExpire not called yet
}

type Queue struct {
	mu      sync.Mutex
	jobs    map[string]*job
	pending []*job         // FIFO of queued jobs
	leases  map[string]int // live leases per worker
	wake    chan struct{}  // closed and replaced whenever a job is queued or a lease ends

	// OnExpire, if set, is called (without the lock held) when a job's
	// deadline passes before it finished; workerID is "" for a queued job.
	OnExpire func(jobID, model, workerID string)
}

func New() *Queue {
	return &Queue{jobs: map[string]*job{}, leases: map[string]int{}, wake: make(chan struct{})}
}

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
func (q *Queue) Submit(req protocol.SubmitRequest) string {
	ttl := time.Duration(req.DeadlineMS) * time.Millisecond
	j := &job{
		req:      req,
		id:       newID(),
		deadline: time.Now().Add(ttl),
		state:    protocol.StateQueued,
		done:     make(chan struct{}),
	}
	q.mu.Lock()
	q.jobs[j.id] = j
	q.pending = append(q.pending, j)
	q.wakeLocked()
	q.mu.Unlock()
	time.AfterFunc(ttl, func() { q.expire(j) })
	return j.id
}

// Claim blocks until a queued job for one of models is available and workerID
// holds fewer than capacity live leases, then leases the job to workerID. It
// returns nil, nil when ctx ends first.
func (q *Queue) Claim(ctx context.Context, workerID string, models []string, capacity int) (*protocol.Assignment, error) {
	for {
		q.mu.Lock()
		now := time.Now()
		for i, j := range q.pending {
			if q.leases[workerID] >= capacity {
				break
			}
			if !slices.Contains(models, j.req.Model) || !now.Before(j.deadline) {
				continue
			}
			q.pending = slices.Delete(q.pending, i, i+1)
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
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[jobID]
	if !ok || j.state != protocol.StateLeased || j.leaseID != leaseID || j.workerID != workerID || !time.Now().Before(j.deadline) {
		return ErrLease
	}
	q.endLeaseLocked(j)
	if errMsg != "" {
		j.state, j.err = protocol.StateFailed, errMsg
	} else {
		j.state, j.output = protocol.StateDone, output
	}
	q.finishLocked(j)
	return nil
}

// Ack records that the worker holding the live lease received the
// assignment. Acking twice is fine; any other lease gets ErrLease.
func (q *Queue) Ack(jobID, leaseID, workerID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[jobID]
	if !ok || j.state != protocol.StateLeased || j.leaseID != leaseID || j.workerID != workerID || !time.Now().Before(j.deadline) {
		return ErrLease
	}
	j.acked = true
	return nil
}

// RevokeUnacked is Release for a lease that was never acknowledged: it
// requeues the job at the front unless Ack (or a result) came first.
func (q *Queue) RevokeUnacked(jobID, leaseID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
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
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[jobID]
	return ok && q.releaseLocked(j, leaseID)
}

func (q *Queue) releaseLocked(j *job, leaseID string) bool {
	if j.state != protocol.StateLeased || j.leaseID != leaseID || !time.Now().Before(j.deadline) {
		return false
	}
	q.endLeaseLocked(j)
	j.state, j.leaseID, j.workerID = protocol.StateQueued, "", ""
	q.pending = slices.Insert(q.pending, 0, j)
	q.wakeLocked()
	return true
}

// Leases returns the number of live leases workerID holds.
func (q *Queue) Leases(workerID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
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
func (q *Queue) Wait(ctx context.Context, jobID string) (protocol.JobStatus, error) {
	q.mu.Lock()
	j, ok := q.jobs[jobID]
	q.mu.Unlock()
	if !ok {
		return protocol.JobStatus{}, ErrNotFound
	}
	select {
	case <-j.done:
	case <-ctx.Done():
	}
	q.mu.Lock()
	// The caller's wait is usually clamped to the deadline, so it can end a hair
	// before the expiry timer fires; a past deadline is expired either way.
	if !time.Now().Before(j.deadline) {
		q.expireLocked(j)
	}
	st := protocol.JobStatus{JobID: j.id, State: j.state, Output: j.output, Error: j.err}
	q.mu.Unlock()
	q.notify(j)
	return st, nil
}

// Deadline returns the job's deadline.
func (q *Queue) Deadline(jobID string) (time.Time, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[jobID]
	if !ok {
		return time.Time{}, ErrNotFound
	}
	return j.deadline, nil
}

// Pending returns the number of queued jobs.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
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
	j.state = protocol.StateExpired
	j.expireUnreported = true
	q.finishLocked(j)
}

// notify calls OnExpire once for a job expireLocked has just expired.
func (q *Queue) notify(j *job) {
	q.mu.Lock()
	report := j.expireUnreported
	j.expireUnreported = false
	id, m, w := j.id, j.req.Model, j.workerID
	q.mu.Unlock()
	if report && q.OnExpire != nil {
		q.OnExpire(id, m, w)
	}
}

func (q *Queue) finishLocked(j *job) {
	close(j.done)
	time.AfterFunc(Retention, func() {
		q.mu.Lock()
		delete(q.jobs, j.id)
		q.mu.Unlock()
	})
}
