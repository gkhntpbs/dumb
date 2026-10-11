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
	done     chan struct{} // closed when state becomes done, failed or expired
}

type Queue struct {
	mu      sync.Mutex
	jobs    map[string]*job
	pending []*job        // FIFO of queued jobs
	wake    chan struct{} // closed and replaced whenever a job is queued
}

func New() *Queue {
	return &Queue{jobs: map[string]*job{}, wake: make(chan struct{})}
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
	close(q.wake)
	q.wake = make(chan struct{})
	q.mu.Unlock()
	time.AfterFunc(ttl, func() { q.expire(j) })
	return j.id
}

// Claim blocks until a queued job for one of models is available, then leases
// it to workerID. It returns nil, nil when ctx ends first.
func (q *Queue) Claim(ctx context.Context, workerID string, models []string) (*protocol.Assignment, error) {
	for {
		q.mu.Lock()
		for i, j := range q.pending {
			if !slices.Contains(models, j.req.Model) {
				continue
			}
			q.pending = slices.Delete(q.pending, i, i+1)
			j.state = protocol.StateLeased
			j.leaseID = newID()
			j.workerID = workerID
			a := &protocol.Assignment{
				JobID:          j.id,
				LeaseID:        j.leaseID,
				LeaseExpiresAt: j.deadline,
				Model:          j.req.Model,
				Prompt:         j.req.Prompt,
				System:         j.req.System,
				Format:         j.req.Format,
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
	if !ok || j.state != protocol.StateLeased || j.leaseID != leaseID || j.workerID != workerID {
		return ErrLease
	}
	if errMsg != "" {
		j.state, j.err = protocol.StateFailed, errMsg
	} else {
		j.state, j.output = protocol.StateDone, output
	}
	q.finishLocked(j)
	return nil
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
	defer q.mu.Unlock()
	return protocol.JobStatus{JobID: j.id, State: j.state, Output: j.output, Error: j.err}, nil
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
	defer q.mu.Unlock()
	switch j.state {
	case protocol.StateQueued:
		if i := slices.Index(q.pending, j); i >= 0 {
			q.pending = slices.Delete(q.pending, i, i+1)
		}
	case protocol.StateLeased:
	default:
		return
	}
	j.state = protocol.StateExpired
	q.finishLocked(j)
}

func (q *Queue) finishLocked(j *job) {
	close(j.done)
	time.AfterFunc(Retention, func() {
		q.mu.Lock()
		delete(q.jobs, j.id)
		q.mu.Unlock()
	})
}
