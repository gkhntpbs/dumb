package protocol

import "time"

type AdminSummary struct {
	SnapshotAt               time.Time `json:"snapshot_at"`
	Epoch                    time.Time `json:"epoch"`
	WorkersOnline            int       `json:"workers_online"`
	Capacity                 int       `json:"capacity"`
	ActiveSlots              int       `json:"active_slots"`
	FreeSlots                int       `json:"free_slots"`
	Queued                   int       `json:"queued"`
	Leased                   int       `json:"leased"`
	Done                     int       `json:"done"`
	Failed                   int       `json:"failed"`
	Expired                  int       `json:"expired"`
	Cancelled                int       `json:"cancelled"`
	Models                   []string  `json:"models"`
	OldestQueuedMS           int       `json:"oldest_queued_ms"`
	LatencySamples           int       `json:"latency_samples"`
	MeanWaitMS               *float64  `json:"mean_wait_ms"`
	MeanCompletionMS         *float64  `json:"mean_completion_ms"`
	MeasurementWindowSeconds int       `json:"measurement_window_seconds"`
}

type AdminWorker struct {
	WorkerID      string    `json:"worker_id"`
	WorkerVersion string    `json:"worker_version"`
	Models        []string  `json:"models"`
	Capacity      int       `json:"capacity"`
	LiveLeases    int       `json:"live_leases"`
	FreeSlots     int       `json:"free_slots"`
	LastSeen      time.Time `json:"last_seen"`
	State         string    `json:"state"`
}

type AdminJob struct {
	JobID      string     `json:"job_id"`
	Model      string     `json:"model"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	DeadlineAt time.Time  `json:"deadline_at"`
	LeasedAt   *time.Time `json:"leased_at,omitempty"`
	WorkerID   string     `json:"worker_id,omitempty"`
	WaitMS     *int       `json:"wait_ms,omitempty"`
}

type AdminWorkers struct {
	SnapshotAt time.Time     `json:"snapshot_at"`
	Epoch      time.Time     `json:"epoch"`
	Total      int           `json:"total"`
	Offset     int           `json:"offset"`
	Limit      int           `json:"limit"`
	Items      []AdminWorker `json:"items"`
}

type AdminQueue struct {
	SnapshotAt time.Time  `json:"snapshot_at"`
	Epoch      time.Time  `json:"epoch"`
	Total      int        `json:"total"`
	Offset     int        `json:"offset"`
	Limit      int        `json:"limit"`
	Items      []AdminJob `json:"items"`
}

type AdminSession struct {
	Authenticated bool `json:"authenticated"`
}

type AdminLogin struct {
	Password string `json:"password"`
}
