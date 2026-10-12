package worker

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestStatusDeliveryCannotReorderSnapshots(t *testing.T) {
	first, release, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var last atomic.Int32
	w := &Worker{models: []string{"fixture"}}
	w.cfg.OnStatus = func(status Status) {
		if calls.Add(1) == 1 {
			close(first)
			<-release
		}
		last.Store(int32(status.Active))
	}
	w.active.Store(1)
	firstDone := make(chan struct{})
	go func() { w.reportStatus(); close(firstDone) }()
	<-first
	w.active.Store(0)
	go func() { w.reportStatus(); close(secondDone) }()
	time.Sleep(10 * time.Millisecond)
	close(release)
	<-firstDone
	<-secondDone
	if last.Load() != 0 {
		t.Fatal("stale active count delivered after completion")
	}
}
