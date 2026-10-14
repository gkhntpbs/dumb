package server

import (
	"github.com/gkhntpbs/dumb/internal/protocol"
	"github.com/gkhntpbs/dumb/internal/queue"
)

func submitForTest(q *queue.Queue, req protocol.SubmitRequest) string {
	id, err := q.Submit("caller", req)
	if err != nil {
		panic(err)
	}
	return id
}
