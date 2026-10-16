package queue

import (
	"errors"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

const MaxResultBytes = protocol.MaxResultBytes

type Limits struct {
	MaxActiveJobs       int
	MaxJobs             int
	MaxBytes            int
	CallerMaxActiveJobs int
	CallerMaxJobs       int
	CallerMaxBytes      int
}

func DefaultLimits() Limits {
	return Limits{64, 256, 64 << 20, 16, 64, 16 << 20}
}

func (l Limits) Normalize() (Limits, error) {
	d := DefaultLimits()
	fields := []*int{&l.MaxActiveJobs, &l.MaxJobs, &l.MaxBytes, &l.CallerMaxActiveJobs, &l.CallerMaxJobs, &l.CallerMaxBytes}
	defaults := []int{d.MaxActiveJobs, d.MaxJobs, d.MaxBytes, d.CallerMaxActiveJobs, d.CallerMaxJobs, d.CallerMaxBytes}
	for i, field := range fields {
		if *field < 0 {
			return Limits{}, errors.New("queue limits must be positive")
		}
		if *field == 0 {
			*field = defaults[i]
		}
	}
	if l.MaxBytes <= MaxResultBytes || l.CallerMaxBytes <= MaxResultBytes {
		return Limits{}, errors.New("queue byte limits must exceed the maximum result reservation")
	}
	if l.MaxActiveJobs > l.MaxJobs || l.CallerMaxActiveJobs > l.CallerMaxJobs || l.CallerMaxActiveJobs > l.MaxActiveJobs || l.CallerMaxJobs > l.MaxJobs || l.CallerMaxBytes > l.MaxBytes {
		return Limits{}, errors.New("queue caller and active limits exceed their enclosing limits")
	}
	return l, nil
}

type usage struct{ active, jobs, bytes int }

type expiration struct{ id, model, worker string }

func requestBytes(owner string, reqModel, prompt, system, format string) int {
	return len(owner) + len(reqModel) + len(prompt) + len(system) + len(format)
}

func (q *Queue) cleanupLocked() {
	if q.closed || (q.store != nil && q.store.poisoned) {
		return
	}
	now := q.now()
	for _, j := range q.jobs {
		if j.state == protocol.StateQueued || j.state == protocol.StateLeased {
			if !now.Before(j.deadline) {
				q.expireLocked(j)
			}
		}
		q.expireAckLocked(j)
		if !j.retainUntil.IsZero() && !now.Before(j.retainUntil) {
			q.removeLocked(j)
		}
	}
}

func (q *Queue) removeLocked(j *job) {
	if q.jobs[j.id] != j {
		return
	}
	if err := q.store.delete(j.id); err != nil {
		q.haltLocked()
		return
	}
	if j.key != "" {
		delete(q.keys, retryKey{j.owner, j.key})
	}
	delete(q.jobs, j.id)
	if j.retentionTimer != nil {
		j.retentionTimer.Stop()
	}
	q.total.jobs--
	q.total.bytes -= j.bytes
	u := q.callers[j.owner]
	u.jobs--
	u.bytes -= j.bytes
	if u.jobs == 0 {
		delete(q.callers, j.owner)
	}
}

func (q *Queue) flushExpirations() {
	q.mu.Lock()
	reports := q.reports
	q.reports = nil
	q.mu.Unlock()
	if q.OnExpire != nil {
		for _, r := range reports {
			q.OnExpire(r.id, r.model, r.worker)
		}
	}
}

func boundedRequestBytes(limit int, fields ...string) (int, bool) {
	if limit < 0 {
		return 0, false
	}
	total := 0
	for _, field := range fields {
		if len(field) > limit-total {
			return 0, false
		}
		total += len(field)
	}
	return total, true
}
