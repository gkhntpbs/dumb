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
	// StateCancelled: the owning caller cancelled the job before it finished.
	StateCancelled = "cancelled"
)

// MaxDeadlineMS bounds SubmitRequest.DeadlineMS.
const MaxDeadlineMS = 600000

const MaxNumPredict = 128

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
	// Features lists optional protocol behaviours this worker supports
	// (FeatureAck). Absent in older workers.
	Features []string `json:"features,omitempty"`
}

// FeatureAck: the worker acknowledges every assignment with
// POST /v1/jobs/{id}/ack, and the server may revoke an unacknowledged lease.
const FeatureAck = "ack"

// AckRequest acknowledges an assignment.
type AckRequest struct {
	LeaseID string `json:"lease_id"`
}

type HelloResponse struct {
	WorkerID         string `json:"worker_id"`
	MinWorkerVersion string `json:"min_worker_version"`
	MaxWait          string `json:"max_wait"`
	// RecommendedWorkerVersion, when set, is the version the operator wants
	// workers to run. A worker older than it checks its signed release source
	// at once; the server never supplies binaries or download locations.
	RecommendedWorkerVersion string `json:"recommended_worker_version,omitempty"`
}

// WhoAmI answers GET /v1/workers/me, a side-effect-free credential and version check.
type WhoAmI struct {
	WorkerID                 string `json:"worker_id"`
	MinWorkerVersion         string `json:"min_worker_version"`
	RecommendedWorkerVersion string `json:"recommended_worker_version,omitempty"`
}

type SubmitRequest struct {
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	System     string `json:"system,omitempty"`
	Format     string `json:"format,omitempty"`
	NumPredict int    `json:"num_predict,omitempty"`
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
	NumPredict     int       `json:"num_predict"`
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

// ReleaseVersion is a SemVer with an optional pre-release, e.g. "0.2.0-beta.1".
// Release tags and X-DUMB-Worker-Version use it; a pre-release sorts below
// the release it precedes (0.2.0-beta.1 < 0.2.0), as in semver.org §11.
type ReleaseVersion struct {
	SemVer
	Pre string // dot-separated identifiers; "" for a release
}

// ParseReleaseVersion accepts everything ParseSemVer accepts, plus "-<pre-release>".
// Build metadata ("+...") is rejected.
func ParseReleaseVersion(s string) (ReleaseVersion, error) {
	core, pre, hasPre := strings.Cut(strings.TrimPrefix(s, "v"), "-")
	sv, err := ParseSemVer(core)
	if err != nil {
		return ReleaseVersion{}, fmt.Errorf("version %q: want MAJOR.MINOR.PATCH[-PRERELEASE]", s)
	}
	if hasPre {
		for _, id := range strings.Split(pre, ".") {
			if id == "" || strings.Trim(id, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-") != "" {
				return ReleaseVersion{}, fmt.Errorf("version %q: bad pre-release %q", s, pre)
			}
		}
	}
	return ReleaseVersion{SemVer: sv, Pre: pre}, nil
}

// Compare returns -1, 0 or +1 as v is older than, equal to or newer than o.
func (v ReleaseVersion) Compare(o ReleaseVersion) int {
	switch {
	case v.SemVer.Less(o.SemVer):
		return -1
	case o.SemVer.Less(v.SemVer):
		return 1
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	a, b := strings.Split(v.Pre, "."), strings.Split(o.Pre, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// compareIdent orders pre-release identifiers: numeric ones numerically and
// below alphanumeric ones, alphanumeric ones in ASCII order.
func compareIdent(a, b string) int {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil:
		return cmpInt(na, nb)
	case ea == nil:
		return -1
	case eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Less reports whether v is older than o.
func (v ReleaseVersion) Less(o ReleaseVersion) bool { return v.Compare(o) < 0 }

// IsPrerelease reports whether v carries a pre-release suffix.
func (v ReleaseVersion) IsPrerelease() bool { return v.Pre != "" }

func (v ReleaseVersion) String() string {
	if v.Pre == "" {
		return v.SemVer.String()
	}
	return v.SemVer.String() + "-" + v.Pre
}
