package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gkhntpbs/dumb/internal/protocol"
)

func durableQueue(t *testing.T, directory string, now func() time.Time) *Queue {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable server storage requires Linux or macOS")
	}
	q, err := Open(Limits{}, StoreConfig{Directory: directory}, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

func TestDurableRestartEveryStateAndCallerKey(t *testing.T) {
	for _, state := range []string{protocol.StateQueued, protocol.StateLeased, protocol.StateDone, protocol.StateFailed, protocol.StateExpired} {
		t.Run(state, func(t *testing.T) {
			directory := privateStateDirectory(t)
			now := time.Now()
			clock := func() time.Time { return now }
			q := durableQueue(t, directory, clock)
			request := req(time.Minute)
			id, err := q.SubmitKey("a", "same-key", request)
			if err != nil {
				t.Fatal(err)
			}
			original := q.jobs[id].deadline
			var assignment *protocol.Assignment
			if state != protocol.StateQueued && state != protocol.StateExpired {
				assignment, err = q.ClaimWithAck(context.Background(), "worker", models, 1, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if err = q.Ack(id, assignment.LeaseID, "worker"); err != nil {
					t.Fatal(err)
				}
				if state == protocol.StateDone {
					err = q.Complete(id, assignment.LeaseID, "worker", "private output", "")
				}
				if state == protocol.StateFailed {
					err = q.Complete(id, assignment.LeaseID, "worker", "", "private error")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if state == protocol.StateExpired {
				now = now.Add(time.Minute)
				_, _ = q.Deadline("a", id)
			}
			if err = q.Close(); err != nil {
				t.Fatal(err)
			}
			q = durableQueue(t, directory, clock)
			replay, err := q.SubmitKey("a", "same-key", request)
			if err != nil || replay != id || !q.jobs[id].deadline.Equal(original) || q.jobs[id].state != state {
				t.Fatalf("restart replay: %q %v %+v", replay, err, q.jobs[id])
			}
			if _, err = q.Deadline("b", id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("owner leaked: %v", err)
			}
			different := request
			different.Prompt = "changed"
			if _, err = q.SubmitKey("a", "same-key", different); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("conflict: %v", err)
			}
			if state == protocol.StateLeased {
				if q.Leases("worker") != 1 {
					t.Fatal("lease lost on restart")
				}
				if err = q.Complete(id, assignment.LeaseID, "other-worker", "forged", ""); !errors.Is(err, ErrLease) {
					t.Fatal(err)
				}
				if err = q.Complete(id, assignment.LeaseID, "worker", "recovered", ""); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDurableAckWindowAndLeaseSurviveRestart(t *testing.T) {
	directory := privateStateDirectory(t)
	now := time.Now()
	clock := func() time.Time { return now }
	q := durableQueue(t, directory, clock)
	id, err := q.SubmitKey("a", "retry", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := q.ClaimWithAck(context.Background(), "w", models, 1, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	due := q.jobs[id].ackDue
	q.Close()
	now = now.Add(time.Second)
	q = durableQueue(t, directory, clock)
	if q.jobs[id].state != protocol.StateLeased || !q.jobs[id].ackDue.Equal(due) {
		t.Fatal("lease or due time reset")
	}
	q.Close()
	now = now.Add(time.Second)
	q = durableQueue(t, directory, clock)
	if q.jobs[id].state != protocol.StateQueued || q.Leases("w") != 0 {
		t.Fatal("overdue unacked lease not revoked")
	}
	if err = q.Ack(id, assignment.LeaseID, "w"); !errors.Is(err, ErrLease) {
		t.Fatal(err)
	}
	if err = q.Complete(id, assignment.LeaseID, "w", "stale", ""); !errors.Is(err, ErrLease) {
		t.Fatal(err)
	}
	replacement, err := q.ClaimWithAck(context.Background(), "w2", models, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.LeaseID == assignment.LeaseID {
		t.Fatal("lease reused")
	}
	if err = q.Ack(id, replacement.LeaseID, "w2"); err != nil {
		t.Fatal(err)
	}
	q.Close()
	now = now.Add(3 * time.Second)
	q = durableQueue(t, directory, clock)
	if q.jobs[id].state != protocol.StateLeased {
		t.Fatal("acknowledged lease revoked after restart")
	}
	writes := 0
	originalWrite := q.store.write
	q.store.write = func(f *os.File, b []byte) (int, error) { writes++; return originalWrite(f, b) }
	if err = q.Ack(id, replacement.LeaseID, "w2"); err != nil || writes != 0 {
		t.Fatalf("heartbeat wrote storage: %v %d", err, writes)
	}
}

func TestDurableConcurrentKeyAndRetentionReuse(t *testing.T) {
	directory := privateStateDirectory(t)
	now := time.Now()
	q := durableQueue(t, directory, func() time.Time { return now })
	var wg sync.WaitGroup
	ids := make(chan string, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := q.SubmitKey("a", "shared", req(time.Minute))
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate job")
		}
	}
	if q.total.jobs != 1 {
		t.Fatal("duplicate resource allocation")
	}
	other, err := q.SubmitKey("b", "shared", req(time.Minute))
	if err != nil || other == first {
		t.Fatal("key not caller scoped")
	}
	assignment := claimNow(t, q, "w")
	if err = q.Complete(first, assignment.LeaseID, "w", "", ""); err != nil {
		t.Fatal(err)
	}
	now = now.Add(Retention)
	replacement, err := q.SubmitKey("a", "shared", req(time.Minute))
	if err != nil || replacement == first {
		t.Fatalf("retention key reuse: %q %v", replacement, err)
	}
}

func TestDurableStoreCorruptionAndPermissions(t *testing.T) {
	for _, mutation := range []string{"checksum", "version", "truncated", "symlink", "directory-mode", "file-mode", "oversized"} {
		t.Run(mutation, func(t *testing.T) {
			directory := privateStateDirectory(t)
			q := durableQueue(t, directory, time.Now)
			id, err := q.SubmitKey("a", "key", req(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			q.Close()
			path := filepath.Join(directory, "job-"+id+".bin")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "checksum":
				data[len(data)-1] ^= 1
				err = os.WriteFile(path, data, 0600)
			case "version":
				record, e := decodeRecord(data)
				if e != nil {
					t.Fatal(e)
				}
				record.Version = 2
				data, e = encodeRecord(record)
				if e != nil {
					t.Fatal(e)
				}
				err = os.WriteFile(path, data, 0600)
			case "truncated":
				err = os.WriteFile(path, data[:len(data)/2], 0600)
			case "symlink":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink("store.lock", path)
				}
			case "directory-mode":
				err = os.Chmod(directory, 0755)
				t.Cleanup(func() { os.Chmod(directory, 0700) })
			case "file-mode":
				err = os.Chmod(path, 0644)
			case "oversized":
				file, e := os.OpenFile(path, os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				err = file.Truncate(int64(DefaultLimits().MaxBytes) + storeOverhead + 1)
				file.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if recovered, err := Open(Limits{}, StoreConfig{Directory: directory}, time.Now); err == nil {
				recovered.Close()
				t.Fatal("invalid state opened")
			}
		})
	}
}

func TestDurableDirectoryLockAndKnownTemporaryCleanup(t *testing.T) {
	directory := privateStateDirectory(t)
	q := durableQueue(t, directory, time.Now)
	if other, err := Open(Limits{}, StoreConfig{Directory: directory}, time.Now); err == nil {
		other.Close()
		t.Fatal("second owner opened")
	}
	q.Close()
	name := ".job-" + strings.Repeat("a", 32) + ".tmp"
	if err := os.WriteFile(filepath.Join(directory, name), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	q = durableQueue(t, directory, time.Now)
	if _, err := os.Stat(filepath.Join(directory, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan remains: %v", err)
	}
	q.Close()
	if err := os.WriteFile(filepath.Join(directory, "unrelated"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(Limits{}, StoreConfig{Directory: directory}, time.Now); err == nil {
		other.Close()
		t.Fatal("unknown entry accepted")
	}
}

func TestDurableIOFailureNeverAcknowledgesOrMutatesAndPoisons(t *testing.T) {
	for _, failure := range []string{"short-write", "file-sync", "rename", "directory-sync"} {
		t.Run(failure, func(t *testing.T) {
			directory := privateStateDirectory(t)
			q := durableQueue(t, directory, time.Now)
			injected := errors.New("injected")
			switch failure {
			case "short-write":
				q.store.write = func(f *os.File, b []byte) (int, error) { return f.Write(b[:len(b)/2]) }
			case "file-sync":
				q.store.sync = func(*os.File) error { return injected }
			case "rename":
				q.store.rename = func(string, string) error { return injected }
			case "directory-sync":
				q.store.syncDirectory = func() error { return injected }
			}
			id, err := q.SubmitKey("a", "uncertain", req(time.Minute))
			if id != "" || !errors.Is(err, ErrStorage) || q.total.jobs != 0 || !q.store.poisoned {
				t.Fatalf("failed accept: %q %v %+v", id, err, q.total)
			}
			if _, err = q.Submit("b", req(time.Minute)); !errors.Is(err, ErrStorage) {
				t.Fatal("poison accepted")
			}
			if _, err = q.Deadline("a", "unknown"); !errors.Is(err, ErrStorage) {
				t.Fatal("poison read allowed")
			}
			q.Close()
			q = durableQueue(t, directory, time.Now)
			replay, err := q.SubmitKey("a", "uncertain", req(time.Minute))
			if err != nil || replay == "" || q.total.jobs != 1 {
				t.Fatalf("recovery: %q %v %+v", replay, err, q.total)
			}
		})
	}
}

func TestDurableDiskReservationAndBoundedTemporary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable server storage requires Linux or macOS")
	}
	directory := privateStateDirectory(t)
	q, err := Open(Limits{}, StoreConfig{Directory: directory, MaxBytes: 2*int64(MaxResultBytes) + 2*storeOverhead}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	id, err := q.Submit("a", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	before := q.store.used
	if _, err = q.Submit("b", req(time.Minute)); !errors.Is(err, ErrStorageCapacity) || q.store.poisoned || q.store.used != before {
		t.Fatalf("disk saturation: %v used%d", err, q.store.used)
	}
	assignment := claimNow(t, q, "w")
	originalRename := q.store.rename
	q.store.rename = func(a, b string) error {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		temporaries := 0
		for _, entry := range entries {
			if tempName.MatchString(entry.Name()) {
				temporaries++
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.Size() > q.store.maxRecord {
					t.Error("temporary exceeds record bound")
				}
			}
		}
		if temporaries != 1 {
			t.Errorf("temporary count%d", temporaries)
		}
		return originalRename(a, b)
	}
	if err = q.Complete(id, assignment.LeaseID, "w", strings.Repeat("x", MaxResultBytes), ""); err != nil {
		t.Fatalf("reserved completion failed: %v", err)
	}
	if q.store.used > q.store.limit {
		t.Fatal("disk budget exceeded")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected files%d", len(entries))
	}
}

func TestDurableCloseStopsOperationsAndReleasesLock(t *testing.T) {
	directory := privateStateDirectory(t)
	q := durableQueue(t, directory, time.Now)
	id, err := q.Submit("a", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { _, err := q.Wait(context.Background(), "a", id); stopped <- err }()
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-stopped:
		if !errors.Is(err, ErrStorage) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait not stopped")
	}
	if _, err = q.Submit("a", req(time.Minute)); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	durableQueue(t, directory, time.Now)
}

func privateStateDirectory(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestDurableExactReservationCoversClaimAckRestartAndMaximumResult(t *testing.T) {
	directory := privateStateDirectory(t)
	now := time.Now()
	clock := func() time.Time { return now }
	q := durableQueue(t, directory, clock)
	id, err := q.SubmitKey("a", strings.Repeat("k", 64), req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	exact := q.store.used
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = Open(Limits{}, StoreConfig{Directory: directory, MaxBytes: exact}, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	assignment, err := q.ClaimWithAck(context.Background(), strings.Repeat("w", 1024), models, 1, time.Second)
	if err != nil {
		t.Fatalf("reserved claim rejected: %v", err)
	}
	if err = q.Ack(id, assignment.LeaseID, strings.Repeat("w", 1024)); err != nil {
		t.Fatal(err)
	}
	if q.store.used != exact {
		t.Fatalf("active reservation grew: %d != %d", q.store.used, exact)
	}
	q.Close()
	q, err = Open(Limits{}, StoreConfig{Directory: directory, MaxBytes: exact}, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if q.store.used != exact {
		t.Fatal("reservation drifted on restart")
	}
	if err = q.Complete(id, assignment.LeaseID, strings.Repeat("w", 1024), strings.Repeat("\x00", MaxResultBytes), ""); err != nil {
		t.Fatalf("reserved result rejected: %v", err)
	}
}

func TestDurableExpiryCommitsBeforeStateAndClearsStoredPrompt(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			directory := privateStateDirectory(t)
			now := time.Now()
			q := durableQueue(t, directory, func() time.Time { return now })
			id, err := q.Submit("a", req(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			assignment := claimNow(t, q, "w")
			if fail {
				q.store.sync = func(*os.File) error { return errors.New("injected") }
			}
			now = now.Add(time.Minute)
			_, err = q.Deadline("a", id)
			if fail {
				if !errors.Is(err, ErrStorage) || q.jobs[id].state != protocol.StateLeased || q.leases["w"] != 1 {
					t.Fatalf("failed expiry changed memory: %v %+v", err, q.jobs[id])
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(directory, "job-"+id+".bin"))
			if err != nil {
				t.Fatal(err)
			}
			record, err := decodeRecord(data)
			if err != nil || record.State != protocol.StateExpired || record.Request.Prompt != "" || record.LeaseID != assignment.LeaseID {
				t.Fatalf("expiry not committed: %+v %v", record, err)
			}
		})
	}
}

func TestDurableMutationFailuresPreserveAcknowledgedState(t *testing.T) {
	for _, operation := range []string{"claim", "ack", "complete"} {
		t.Run(operation, func(t *testing.T) {
			q := durableQueue(t, privateStateDirectory(t), time.Now)
			id, err := q.Submit("a", req(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			var assignment *protocol.Assignment
			if operation != "claim" {
				assignment, err = q.ClaimWithAck(context.Background(), "w", models, 1, time.Second)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := recordOf(q.jobs[id])
			total := q.total
			q.store.rename = func(string, string) error { return errors.New("injected") }
			switch operation {
			case "claim":
				_, err = q.Claim(context.Background(), "w", models, 1)
			case "ack":
				err = q.Ack(id, assignment.LeaseID, "w")
			case "complete":
				err = q.Complete(id, assignment.LeaseID, "w", "lost", "")
			}
			if !errors.Is(err, ErrStorage) || recordOf(q.jobs[id]) != before || q.total != total {
				t.Fatalf("failed%s mutated: %v %+v", operation, err, q.jobs[id])
			}
		})
	}
}

func TestQueueCloseVolatileAndDurableNoPostCloseMutations(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprint(durable), func(t *testing.T) {
			now := time.Now()
			q := New()
			if durable {
				q = durableQueue(t, privateStateDirectory(t), func() time.Time { return now })
			} else {
				q.SetClock(func() time.Time { return now })
			}
			id, err := q.Submit("a", req(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			assignment := claimNow(t, q, "w")
			if err = q.Complete(id, assignment.LeaseID, "w", "", ""); err != nil {
				t.Fatal(err)
			}
			q.Close()
			now = now.Add(2 * time.Minute)
			q.Observe("", "", 0, 10)
			if _, err = q.Wait(context.Background(), "a", id); !errors.Is(err, ErrStorage) {
				t.Fatal(err)
			}
			if err = q.Complete(id, assignment.LeaseID, "w", "after-close", ""); !errors.Is(err, ErrStorage) {
				t.Fatal(err)
			}
			if q.Release(id, assignment.LeaseID) {
				t.Fatal("released after close")
			}
			if err = q.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDurableCompletionUsesOneValidatedTimestamp(t *testing.T) {
	directory := privateStateDirectory(t)
	start := time.Now()
	q := durableQueue(t, directory, func() time.Time { return start })
	id, err := q.Submit("a", req(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assignment := claimNow(t, q, "w")
	deadline := q.jobs[id].deadline
	calls := 0
	q.SetClock(func() time.Time {
		calls++
		if calls <= 2 {
			return deadline.Add(-time.Nanosecond)
		}
		return deadline.Add(time.Second)
	})
	if err = q.Complete(id, assignment.LeaseID, "w", "last instant", ""); err != nil {
		t.Fatal(err)
	}
	if q.jobs[id].retainUntil.After(deadline.Add(Retention)) {
		t.Fatal("completion retention exceeded durable deadline invariant")
	}
	q.Close()
	recovered := durableQueue(t, directory, func() time.Time { return deadline.Add(time.Second) })
	if recovered.jobs[id].state != protocol.StateDone {
		t.Fatal("acknowledged boundary result lost")
	}
}
