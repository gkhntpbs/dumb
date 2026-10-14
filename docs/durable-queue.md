# Durable jobs and caller-owned retries

`docs/protocol/openapi.yaml` defines the HTTP contract. The optional durable
backend is implemented with the Go standard library and supports Linux and
macOS servers. Configuring it on another platform fails closed. Volatile server
and worker builds remain available on Windows.

## Configuration

Set `DUMB_QUEUE_STATE_DIRECTORY` to an absolute, clean path. The final directory
must be owned by the server user, be a real directory rather than a symlink, and
have mode `0700`. If missing, the server creates that single directory with mode
`0700`; its parent must already exist. Existing directory permissions are never
changed. Ordinary parent aliases such as `/var` on macOS are allowed. Job files,
temporary files and the process lock must be regular, owner-held files with mode
`0600`; symlinks and unexpected entries fail startup.

`DUMB_QUEUE_STATE_MAX_BYTES` is a positive decimal byte count, defaulting to
`268435456` when a directory is configured. A limit without a directory, invalid
integer, or limit not exceeding one result reservation plus metadata fails
startup. Embedders use `Config.QueueStateDirectory` and
`Config.QueueStateMaxBytes` and must call `Server.Close()` after stopping HTTP
handlers. Close is repeatable, stops timers, wakes waiting calls, rejects further
queue operations and releases the process lock. Constructor failure releases its
resources before returning. Recovery receives `Config.Now` before reading jobs.

An empty directory setting explicitly selects volatile compatibility mode. It
provides concurrent idempotency while the process is alive, without a restart
guarantee. A production embedding must configure storage explicitly; this change
does not migrate or enable a live deployment.

## Persistence and recovery

Each job has one file named with its server-generated job ID. A versioned frame
contains a length, SHA-256 checksum and gob-encoded record. The record contains
its owner, request or terminal output, original timestamps, lease holder and ID,
acknowledgement state, FIFO order, idempotency key/digest and disk allocation.
The caller index is rebuilt from these same records, so the job and retry key
cannot be committed independently.

A transition writes one exclusive temporary file inside the private directory,
syncs its contents, closes it, atomically renames it over the job file, and syncs
the directory before publishing the in-memory transition or HTTP response.
Directory creation and the lock are synced during opening. An exclusive OS lock
prevents two server processes from opening the same state directory. No external
database, service or dependency is required.

Queued jobs recover in persisted FIFO order with their original deadlines.
Live leases retain their original worker, lease ID and deadline. The first ack
is persisted; subsequent ack heartbeats perform no storage writes. Ack-capable
assignments persist their original ack due time before delivery. After restart,
an overdue unacknowledged lease returns to the front of the queue; results or
acks using that revoked lease receive `409`. Acknowledged and legacy leases are
preserved until completion or deadline. Worker registrations and server epoch
remain process-local; existing authenticated workers can post results for their
recovered leases.

Terminal results recover until their original retention end. Expired deadlines
become persisted terminal records with cleared prompts; retention cleanup removes
the record and retry key. Restart never grants another execution deadline or
another retention minute. Historical process counters are not a durable audit
log.

Only known unfinished temporary filenames are removed on startup. A corrupt
committed checksum, unsupported version, malformed record, oversized file,
invalid ownership/mode, conflicting index/order or unexpected entry fails
startup. Committed corrupt records are never silently ignored. Tests establish
process-crash recovery, including a killed process after durable submission but
before HTTP delivery; filesystem or device failure beyond successful sync
semantics is not an exactly-once execution guarantee.

## Capacity and failures

Existing global and caller memory admission limits still apply. Durable admission
also reserves the actual encoded file, a one-MiB future result and 4096 bytes of
metadata. Its allocation is stored with the job and stays stable across ordinary
claim, ack, release and restart. Unusually large worker metadata is checked before
a lease is assigned and can require additional available disk capacity. Accepted
results within the one-MiB payload limit use the existing reservation.

The sum of committed file sizes and active reservations never exceeds the
configured state byte budget. At most `MaxJobs` committed job records, one lock
file and one atomic-write temporary file exist. The temporary file is separately
bounded by `min(MaxBytes, QueueStateMaxBytes - 4096) + 4096` bytes. That one-file
scratch allowance is additional to the configured committed/reserved budget.
Filesystem allocation blocks and directory metadata are outside serialized byte
accounting; job counts bound file counts. An oversized future record is refused
before assignment or acceptance.

Predictable queue or disk capacity exhaustion returns `503` without poisoning
the store or accepting a job. A write, short write, sync, close, rename or
retention-delete uncertainty poisons the queue until restart. Caller reads and
submit/claim/ack/result operations then return sanitized `503` errors; no failed
transition is acknowledged or applied to memory. Recovery may find a fully
committed transition whose response was lost, which is why callers retain their
idempotency key. Do not repair a state directory by deleting unknown job files.

## Idempotency

`POST /v1/jobs` accepts an optional `Idempotency-Key` matching
`^[A-Za-z0-9._:-]{1,64}$`. It is scoped to the authenticated caller principal,
never to a caller-supplied owner. The SHA-256 payload digest covers the normalized
model, prompt, system, format, num_predict and deadline_ms. Omitted or zero
num_predict equals 128. The wait query, request ID and unknown body fields do not
change identity.

A matching caller/key/request returns the existing job ID, state and deadline
before checking online workers or admission capacity. A different normalized
request using that key receives `409`; another caller can independently use the
same key. The key lives exactly as long as its job, including one minute of
terminal retention. Once retention ends, the key can create a new job. A missing
key always requests a new job.

The TypeScript caller exposes `idempotencyKey` on `submit` and `execute`. It sends
the key only on submission, validates it using generated contract limits, and
never invents a key or retries automatically. Consumers keep the same key and
payload when resolving an ambiguous submission; a fresh key requests fresh work.

```ts
const key = crypto.randomUUID();
await client.submit(request, { idempotencyKey: key });
```

Verification commands and red/green receipts are in
`docs/qa/durable-queue-2026-10-15.md`.
