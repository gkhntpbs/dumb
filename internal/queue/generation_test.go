package queue

import (
	"context"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func TestExpiredLeaseRejectedBeforeTimer(t *testing.T) {
	for _, operation := range []string{"ack", "complete"} {
		t.Run(operation, func(t *testing.T) {
			q := New()
			id := q.Submit(protocol.SubmitRequest{Model: "m", Prompt: "p", DeadlineMS: 5000})
			a, _ := q.Claim(context.Background(), "w", []string{"m"}, 1)
			q.mu.Lock()
			q.jobs[id].deadline = time.Now().Add(-time.Second)
			q.mu.Unlock()
			var err error
			if operation == "ack" {
				err = q.Ack(id, a.LeaseID, "w")
			} else {
				err = q.Complete(id, a.LeaseID, "w", "late", "")
			}
			if err != ErrLease {
				t.Fatalf("expired lease accepted: %v", err)
			}
		})
	}
}
