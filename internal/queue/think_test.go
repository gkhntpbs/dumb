package queue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"github.com/gkhntpbs/dumb/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyWorkerCannotClaimExplicitThink(t *testing.T) {
	q := New()
	defer q.Close()
	var req protocol.SubmitRequest
	json.Unmarshal([]byte(`{"model":"m","prompt":"p","deadline_ms":5000,"think":false}`), &req)
	if _, err := q.Submit("caller", req); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if a, err := q.Claim(ctx, "legacy", []string{"m"}, 1); err != nil || a != nil {
		t.Fatalf("legacy assignment=%+v err=%v", a, err)
	}
}

func TestThinkDigestDistinguishesOmittedFalseTrue(t *testing.T) {
	seen := map[[32]byte]bool{}
	for _, text := range []string{`{"model":"m","prompt":"p","deadline_ms":5000}`, `{"model":"m","prompt":"p","deadline_ms":5000,"think":false}`, `{"model":"m","prompt":"p","deadline_ms":5000,"think":true}`} {
		var r protocol.SubmitRequest
		if err := json.Unmarshal([]byte(text), &r); err != nil {
			t.Fatal(err)
		}
		digest := payloadDigest(r)
		if seen[digest] {
			t.Fatal("thinking modes share an idempotency digest")
		}
		seen[digest] = true
	}
}

func TestThinkDurableQueuedAndResultRoundtrip(t *testing.T) {
	for _, mode := range []string{"omitted", "false", "true"} {
		t.Run(mode, func(t *testing.T) {
			directory := privateStateDirectory(t)
			now := time.Now()
			clock := func() time.Time { return now }
			q := durableQueue(t, directory, clock)
			request := req(time.Minute)
			if mode != "omitted" {
				value := mode == "true"
				request.Think = &value
			}
			id, err := q.SubmitKey("caller", "think-replay", request)
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Close(); err != nil {
				t.Fatal(err)
			}
			q = durableQueue(t, directory, clock)
			replay, err := q.SubmitKey("caller", "think-replay", request)
			if err != nil || replay != id {
				t.Fatalf("replay=%q error=%v", replay, err)
			}
			assignment, err := q.ClaimWithFeatures(context.Background(), "w", models, 1, 0, []string{protocol.FeatureThink})
			if err != nil || assignment == nil {
				t.Fatalf("claim=%+v error=%v", assignment, err)
			}
			if mode == "omitted" {
				if assignment.Think != nil {
					t.Fatal("omitted became explicit")
				}
			} else if assignment.Think == nil || *assignment.Think != (mode == "true") {
				t.Fatalf("lost mode %s after restart", mode)
			}
			result := protocol.ResultRequest{LeaseID: assignment.LeaseID, Output: "answer", Thinking: "private reasoning", DoneReason: "stop"}
			if err := q.CompleteResult(id, "w", result); err != nil {
				t.Fatal(err)
			}
			expectedBytes := len("caller") + len(request.Model) + len(result.Output) + len(result.Thinking) + len(result.DoneReason)
			if q.jobs[id].bytes != expectedBytes {
				t.Fatalf("retained bytes=%d want=%d", q.jobs[id].bytes, expectedBytes)
			}
			if err := q.Close(); err != nil {
				t.Fatal(err)
			}
			q = durableQueue(t, directory, clock)
			status, err := q.Wait(context.Background(), "caller", id)
			if err != nil || status.Output != result.Output || status.Thinking != result.Thinking || status.DoneReason != result.DoneReason {
				t.Fatalf("status=%+v error=%v", status, err)
			}
			if q.jobs[id].bytes != expectedBytes {
				t.Fatalf("restored bytes=%d want=%d", q.jobs[id].bytes, expectedBytes)
			}
			if _, err := q.Wait(context.Background(), "other", id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("reasoning leaked across owners: %v", err)
			}
		})
	}
}

func TestThinkPointersCannotMutateQueuedRequest(t *testing.T) {
	q := New()
	defer q.Close()
	value := false
	request := req(time.Minute)
	request.Think = &value
	id, err := q.Submit("caller", request)
	if err != nil {
		t.Fatal(err)
	}
	value = true
	assignment, err := q.ClaimWithFeatures(context.Background(), "w", models, 1, 0, []string{protocol.FeatureThink})
	if err != nil || assignment == nil || assignment.Think == nil || *assignment.Think {
		t.Fatalf("caller mutated request: %+v %v", assignment, err)
	}
	*assignment.Think = true
	if *q.jobs[id].req.Think {
		t.Fatal("assignment mutated queue request")
	}
}

func TestThinkMetadataSharesResultByteLimit(t *testing.T) {
	q := New()
	defer q.Close()
	request := req(time.Minute)
	id, err := q.Submit("caller", request)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := q.Claim(context.Background(), "w", models, 1)
	if err != nil {
		t.Fatal(err)
	}
	result := protocol.ResultRequest{LeaseID: assignment.LeaseID, Output: strings.Repeat("x", MaxResultBytes-2), Thinking: "a", DoneReason: "bc"}
	if err := q.CompleteResult(id, "w", result); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("oversized metadata accepted: %v", err)
	}
	if q.jobs[id].state != protocol.StateLeased {
		t.Fatal("oversized result completed lease")
	}
	result.Output = result.Output[:len(result.Output)-1]
	if err := q.CompleteResult(id, "w", result); err != nil {
		t.Fatalf("boundary rejected: %v", err)
	}
}

type legacyThinkRequest struct {
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	System     string `json:"system,omitempty"`
	Format     string `json:"format,omitempty"`
	NumPredict int    `json:"num_predict,omitempty"`
	DeadlineMS int    `json:"deadline_ms"`
}
type legacyThinkStoredJob struct {
	Version                                        int
	ID, Owner, Key                                 string
	Digest                                         [32]byte
	Request                                        legacyThinkRequest
	Created, Leased, Deadline, RetainUntil, AckDue time.Time
	State, LeaseID, WorkerID, Output, Error        string
	Acked                                          bool
	Order                                          int64
	Allocation                                     int64
}

func TestThinkReadsLegacyGobSnapshotAndIdempotencyDigest(t *testing.T) {
	directory := privateStateDirectory(t)
	now := time.Now()
	clock := func() time.Time { return now }
	request := req(time.Minute)
	request.NumPredict = protocol.MaxNumPredict
	oldRequest := legacyThinkRequest{Model: request.Model, Prompt: request.Prompt, System: request.System, Format: request.Format, NumPredict: request.NumPredict, DeadlineMS: request.DeadlineMS}
	oldJSON, err := json.Marshal(oldRequest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(oldJSON)
	if digest != payloadDigest(request) {
		t.Fatal("omitted think changed legacy idempotency digest")
	}
	id := strings.Repeat("a", 32)
	record := legacyThinkStoredJob{Version: 1, ID: id, Owner: "caller", Key: "legacy-key", Digest: digest, Request: oldRequest, Created: now, Deadline: now.Add(time.Minute), State: protocol.StateQueued, Order: 1, Allocation: 2 * MaxResultBytes}
	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(record); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 44+payload.Len())
	copy(data, "DUMBQ001")
	binary.BigEndian.PutUint32(data[8:12], uint32(payload.Len()))
	checksum := sha256.Sum256(payload.Bytes())
	copy(data[12:44], checksum[:])
	copy(data[44:], payload.Bytes())
	if err := os.WriteFile(filepath.Join(directory, "job-"+id+".bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	q := durableQueue(t, directory, clock)
	replay, err := q.SubmitKey("caller", "legacy-key", request)
	if err != nil || replay != id {
		t.Fatalf("legacy replay=%q error=%v", replay, err)
	}
	if q.jobs[id].req.Think != nil {
		t.Fatal("legacy request gained explicit think")
	}
	assignment, err := q.Claim(context.Background(), "legacy-worker", models, 1)
	if err != nil || assignment == nil || assignment.JobID != id || assignment.Think != nil {
		t.Fatalf("legacy claim=%+v error=%v", assignment, err)
	}
}
