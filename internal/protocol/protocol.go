// Package protocol is the wire contract between dumb-server, dumb-worker and callers.
// docs/protocol/openapi.yaml is the authority; these types follow it.
package protocol

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Version is raised when a field is removed or re-meant, never when one is added.
const Version = 1

// WorkerVersionHeader carries the worker's semantic version on every worker request.
const WorkerVersionHeader = "X-DUMB-Worker-Version"

// Job states.
const (
	StateQueued  = "queued"
	StateLeased  = "leased"
	StateDone    = "done"
	StateFailed  = "failed"
	StateExpired = "expired"
)

// MaxDeadlineMS bounds SubmitRequest.DeadlineMS.
const MaxDeadlineMS = 600000

type Error struct {
	Error string `json:"error"`
}

type UpgradeError struct {
	Error            string `json:"error"`
	MinWorkerVersion string `json:"min_worker_version"`
}

type HelloRequest struct {
	WorkerVersion   string   `json:"worker_version"`
	ProtocolVersion int      `json:"protocol_version"`
	Models          []string `json:"models"`
	Capacity        int      `json:"capacity"`
}

type HelloResponse struct {
	WorkerID         string `json:"worker_id"`
	MinWorkerVersion string `json:"min_worker_version"`
	MaxWait          string `json:"max_wait"`
}

// WhoAmI answers GET /v1/workers/me, a side-effect-free credential and version check.
type WhoAmI struct {
	WorkerID         string `json:"worker_id"`
	MinWorkerVersion string `json:"min_worker_version"`
}

type SubmitRequest struct {
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	System     string `json:"system,omitempty"`
	Format     string `json:"format,omitempty"`
	DeadlineMS int    `json:"deadline_ms"`
}

type Assignment struct {
	JobID          string    `json:"job_id"`
	LeaseID        string    `json:"lease_id"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	Model          string    `json:"model"`
	Prompt         string    `json:"prompt"`
	System         string    `json:"system,omitempty"`
	Format         string    `json:"format,omitempty"`
}

type ResultRequest struct {
	LeaseID string `json:"lease_id"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

type JobStatus struct {
	JobID  string `json:"job_id"`
	State  string `json:"state"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Status struct {
	WorkersOnline int      `json:"workers_online"`
	Models        []string `json:"models"`
}

// SemVer is a parsed MAJOR.MINOR.PATCH version.
type SemVer [3]int

// ParseSemVer accepts "1.2.3" or "v1.2.3".
func ParseSemVer(s string) (SemVer, error) {
	var v SemVer
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("version %q: bad component %q", s, p)
		}
		v[i] = n
	}
	return v, nil
}

// Less reports whether v is older than o.
func (v SemVer) Less(o SemVer) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] < o[i]
		}
	}
	return false
}

func (v SemVer) String() string { return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]) }
