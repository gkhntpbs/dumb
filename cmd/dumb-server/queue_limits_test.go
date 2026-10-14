package main

import (
	"os"
	"testing"

	"github.com/gkhntpbs/dumb/internal/queue"
)

func TestQueueLimitsEnvironment(t *testing.T) {
	keys := []string{"DUMB_QUEUE_MAX_ACTIVE_JOBS", "DUMB_QUEUE_MAX_JOBS", "DUMB_QUEUE_MAX_BYTES", "DUMB_QUEUE_CALLER_MAX_ACTIVE_JOBS", "DUMB_QUEUE_CALLER_MAX_JOBS", "DUMB_QUEUE_CALLER_MAX_BYTES"}
	for _, key := range keys {
		value, set := os.LookupEnv(key)
		os.Unsetenv(key)
		t.Cleanup(func() {
			if set {
				os.Setenv(key, value)
			} else {
				os.Unsetenv(key)
			}
		})
	}
	limits, err := queueLimitsFromEnvironment()
	if err != nil || limits != queue.DefaultLimits() {
		t.Fatalf("defaults: %+v %v", limits, err)
	}
	for _, key := range keys {
		for _, value := range []string{"", "0", "-1", "+1", "1.5", "off", " 2", "99999999999999999999999999999"} {
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Setenv(key, value)
				if _, err := queueLimitsFromEnvironment(); err == nil {
					t.Fatal("invalid accepted")
				}
			})
		}
	}
	t.Setenv("DUMB_QUEUE_MAX_ACTIVE_JOBS", "32")
	t.Setenv("DUMB_QUEUE_CALLER_MAX_ACTIVE_JOBS", "8")
	limits, err = queueLimitsFromEnvironment()
	if err != nil || limits.MaxActiveJobs != 32 || limits.CallerMaxActiveJobs != 8 {
		t.Fatalf("configured: %+v %v", limits, err)
	}
}
