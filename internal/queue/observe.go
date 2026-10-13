package queue

import (
	"container/heap"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

type measurement struct {
	at               time.Time
	wait, completion float64
}
type Observation struct {
	At                                                    time.Time
	Queued, Leased, Done, Failed, Expired, OldestQueuedMS int
	Samples                                               int
	MeanWait, MeanCompletion                              *float64
	Leases                                                map[string]int
	Jobs                                                  []protocol.AdminJob
	Total                                                 int
}

type jobHeap []protocol.AdminJob

func (h jobHeap) Len() int { return len(h) }
func (h jobHeap) Less(i, j int) bool {
	if h[i].CreatedAt.Equal(h[j].CreatedAt) {
		return h[i].JobID < h[j].JobID
	}
	return h[i].CreatedAt.Before(h[j].CreatedAt)
}
func (h jobHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *jobHeap) Push(v any)   { *h = append(*h, v.(protocol.AdminJob)) }
func (h *jobHeap) Pop() any     { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }

func (q *Queue) recordLocked(j *job, now time.Time) {
	if len(q.samples) == 256 {
		copy(q.samples, q.samples[1:])
		q.samples = q.samples[:255]
	}
	q.samples = append(q.samples, measurement{now, float64(j.leased.Sub(j.created).Microseconds()) / 1000, float64(now.Sub(j.created).Microseconds()) / 1000})
}

func (q *Queue) Observe(model, state string, offset, limit int) Observation {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now().UTC()
	o := Observation{At: now, Done: q.completed, Failed: q.failed, Expired: q.expired, Leases: map[string]int{}, Jobs: []protocol.AdminJob{}}
	h := &jobHeap{}
	for _, j := range q.jobs {
		current := j.state
		if (current == protocol.StateQueued || current == protocol.StateLeased) && !now.Before(j.deadline) {
			current = protocol.StateExpired
		}
		switch current {
		case protocol.StateQueued:
			o.Queued++
			o.OldestQueuedMS = max(o.OldestQueuedMS, int(now.Sub(j.created).Milliseconds()))
		case protocol.StateLeased:
			o.Leased++
			o.Leases[j.workerID]++
		case protocol.StateExpired:
			if j.state != protocol.StateExpired {
				o.Expired++
			}
		}
		if model != "" && j.req.Model != model || state != "" && current != state {
			continue
		}
		o.Total++
		if limit == 0 {
			continue
		}
		row := protocol.AdminJob{JobID: j.id, Model: boundedText(j.req.Model, 128), State: current, CreatedAt: j.created, DeadlineAt: j.deadline}
		if !j.leased.IsZero() {
			t := j.leased
			row.LeasedAt = &t
			row.WorkerID = boundedText(j.workerID, 128)
		}
		if current == protocol.StateQueued || !j.leased.IsZero() {
			wait := int(now.Sub(j.created).Milliseconds())
			if !j.leased.IsZero() {
				wait = int(j.leased.Sub(j.created).Milliseconds())
			}
			row.WaitMS = &wait
		}
		heap.Push(h, row)
		if h.Len() > offset+limit {
			heap.Pop(h)
		}
	}
	n := h.Len()
	rows := make([]protocol.AdminJob, n)
	for i := n - 1; i >= 0; i-- {
		rows[i] = heap.Pop(h).(protocol.AdminJob)
	}
	if offset < n {
		o.Jobs = rows[offset:]
	}
	var wait, completion float64
	for _, m := range q.samples {
		if now.Sub(m.at) <= 300*time.Second {
			o.Samples++
			wait += m.wait
			completion += m.completion
		}
	}
	if o.Samples > 0 {
		wait /= float64(o.Samples)
		completion /= float64(o.Samples)
		o.MeanWait = &wait
		o.MeanCompletion = &completion
	}
	return o
}

func boundedText(v string, limit int) string {
	r := []rune(v)
	if len(r) > limit {
		r = r[:limit]
	}
	return string(r)
}
