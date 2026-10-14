package queue

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
var ErrIdempotencyConflict = errors.New("idempotency key conflicts with an existing request")
var ErrIdempotencyKey = errors.New("invalid idempotency key")

type retryKey struct{ owner, key string }

func active(j *job) bool { return j.state == protocol.StateQueued || j.state == protocol.StateLeased }

func payloadDigest(req protocol.SubmitRequest) [32]byte {
	if req.NumPredict == 0 {
		req.NumPredict = protocol.MaxNumPredict
	}
	data, _ := json.Marshal(req)
	return sha256.Sum256(data)
}

func Open(limits Limits, cfg StoreConfig, now func() time.Time) (*Queue, error) {
	limits, err := limits.Normalize()
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	store, records, err := openStateStore(cfg, limits.MaxJobs, limits.MaxBytes)
	if err != nil {
		return nil, err
	}
	q := &Queue{limits: limits, now: now, store: store, callers: map[string]*usage{}, jobs: map[string]*job{}, leases: map[string]int{}, keys: map[retryKey]*job{}, wake: make(chan struct{}), stop: make(chan struct{}), front: -1}
	q.mu.Lock()
	defer q.mu.Unlock()
	orders := map[int64]bool{}
	for _, r := range records {
		if orders[r.Order] {
			q.closeLocked()
			return nil, ErrStorage
		}
		orders[r.Order] = true
		j := &job{diskCharge: r.Allocation, id: r.ID, owner: r.Owner, key: r.Key, digest: r.Digest, req: r.Request, created: r.Created, leased: r.Leased, deadline: r.Deadline, retainUntil: r.RetainUntil, ackDue: r.AckDue, state: r.State, leaseID: r.LeaseID, workerID: r.WorkerID, output: r.Output, err: r.Error, acked: r.Acked, order: r.Order, done: make(chan struct{})}
		j.bytes = requestBytes(j.owner, j.req.Model, j.req.Prompt, j.req.System, j.req.Format)
		if active(j) {
			j.bytes += MaxResultBytes
		} else {
			j.bytes += len(j.output) + len(j.err)
			close(j.done)
		}
		if j.key != "" {
			key := retryKey{j.owner, j.key}
			if q.keys[key] != nil {
				q.closeLocked()
				return nil, ErrStorage
			}
			q.keys[key] = j
		}
		q.jobs[j.id] = j
		u := q.callers[j.owner]
		if u == nil {
			u = &usage{}
			q.callers[j.owner] = u
		}
		q.total.jobs++
		q.total.bytes += j.bytes
		u.jobs++
		u.bytes += j.bytes
		if active(j) {
			q.total.active++
			u.active++
		}
		if j.state == protocol.StateQueued {
			q.pending = append(q.pending, j)
		}
		if j.state == protocol.StateLeased {
			q.leases[j.workerID]++
		}
		q.sequence = max(q.sequence, j.order)
		q.front = min(q.front, j.order-1)
	}
	q.cleanupLocked()
	if q.storageErrorLocked() != nil {
		q.closeLocked()
		return nil, ErrStorage
	}
	if q.total.active > limits.MaxActiveJobs || q.total.jobs > limits.MaxJobs || q.total.bytes > limits.MaxBytes {
		q.closeLocked()
		return nil, ErrCapacity
	}
	for _, u := range q.callers {
		if u.active > limits.CallerMaxActiveJobs || u.jobs > limits.CallerMaxJobs || u.bytes > limits.CallerMaxBytes {
			q.closeLocked()
			return nil, ErrCallerCapacity
		}
	}
	for _, j := range q.jobs {
		if active(j) {
			q.scheduleDeadlineLocked(j)
			q.scheduleAckLocked(j)
		} else {
			q.scheduleRetentionLocked(j)
		}
	}
	return q, nil
}

func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closeLocked()
}
func (q *Queue) closeLocked() error {
	if q.closed {
		return nil
	}
	q.closed = true
	for _, j := range q.jobs {
		if j.deadlineTimer != nil {
			j.deadlineTimer.Stop()
		}
		if j.retentionTimer != nil {
			j.retentionTimer.Stop()
		}
		if j.ackTimer != nil {
			j.ackTimer.Stop()
		}
	}
	q.haltLocked()
	return q.store.close()
}

func (q *Queue) haltLocked() {
	if !q.halted {
		q.halted = true
		close(q.stop)
		q.wakeLocked()
	}
}
func (q *Queue) storageErrorLocked() error {
	if q.closed || (q.store != nil && q.store.poisoned) {
		q.haltLocked()
		return ErrStorage
	}
	return nil
}
func (q *Queue) persistLocked(j *job) error {
	if err := q.storageErrorLocked(); err != nil {
		return err
	}
	err := q.store.save(j)
	if errors.Is(err, ErrStorage) {
		q.haltLocked()
	}
	return err
}
func (q *Queue) replayLocked(owner, key string, req protocol.SubmitRequest) (string, bool, error) {
	if err := q.storageErrorLocked(); err != nil {
		return "", false, err
	}
	if owner == "" {
		return "", false, ErrOwner
	}
	if key == "" {
		return "", false, nil
	}
	if !idempotencyKey.MatchString(key) {
		return "", false, ErrIdempotencyKey
	}
	if j := q.keys[retryKey{owner, key}]; j != nil {
		if j.digest != payloadDigest(req) {
			return "", false, ErrIdempotencyConflict
		}
		return j.id, true, nil
	}
	return "", false, nil
}
func (q *Queue) Replay(owner, key string, req protocol.SubmitRequest) (string, bool, error) {
	defer q.flushExpirations()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cleanupLocked()
	return q.replayLocked(owner, key, req)
}
func (q *Queue) scheduleDeadlineLocked(j *job) {
	if j.deadlineTimer != nil {
		j.deadlineTimer.Stop()
	}
	j.deadlineTimer = time.AfterFunc(max(j.deadline.Sub(q.now()), 0), func() { q.expire(j) })
}
func (q *Queue) scheduleAckLocked(j *job) {
	if j.ackTimer != nil {
		j.ackTimer.Stop()
		j.ackTimer = nil
	}
	if j.state == protocol.StateLeased && !j.acked && !j.ackDue.IsZero() {
		id, lease := j.id, j.leaseID
		j.ackTimer = time.AfterFunc(max(j.ackDue.Sub(q.now()), 0), func() { q.RevokeUnacked(id, lease) })
	}
}
func (q *Queue) scheduleRetentionLocked(j *job) {
	if j.retentionTimer != nil {
		j.retentionTimer.Stop()
	}
	j.retentionTimer = time.AfterFunc(max(j.retainUntil.Sub(q.now()), 0), func() {
		q.mu.Lock()
		if !q.closed {
			q.removeLocked(j)
		}
		q.mu.Unlock()
	})
}
func (q *Queue) prepareTerminal(j *job) job { return q.prepareTerminalAt(j, q.now()) }
func (q *Queue) prepareTerminalAt(j *job, finished time.Time) job {
	next := *j
	next.req.Prompt = ""
	next.req.System = ""
	next.req.Format = ""
	next.req.NumPredict = 0
	next.bytes = len(next.owner) + len(next.req.Model) + len(next.output) + len(next.err)
	if next.state == protocol.StateExpired {
		finished = next.deadline
	}
	if next.retainUntil.IsZero() {
		next.retainUntil = finished.Add(Retention)
	}
	return next
}
func (q *Queue) expireAckLocked(j *job) {
	if j.state == protocol.StateLeased && !j.acked && !j.ackDue.IsZero() && !q.now().Before(j.ackDue) {
		q.releaseLocked(j, j.leaseID)
	}
}
func (q *Queue) sortPendingLocked() {
	slices.SortFunc(q.pending, func(a, b *job) int {
		if a.order < b.order {
			return -1
		}
		if a.order > b.order {
			return 1
		}
		return 0
	})
}
