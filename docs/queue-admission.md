# Queue admission and caller ownership

The queue is in memory. This change does not add durability, idempotent
submission, cancellation or a persistence guarantee across server restarts.
The authority for HTTP behavior is `docs/protocol/openapi.yaml`.

Every accepted job belongs to the authenticated caller principal ID. The request
cannot select or override that ID. A replacement credential for the same
principal can still read its jobs. Another caller receives the same immediate
`404` error as an unknown job, before a requested wait begins. This applies to
queued, leased, done, failed and expired jobs. Worker acknowledgement and result
submission continue to use the existing worker ID and live lease checks.

Admission under one queue mutex checks caller limits before aggregate limits.
A caller limit returns `429`; an aggregate limit returns `503`. Both capacity
responses contain `Retry-After: 1`, which suggests a later attempt without
promising capacity. A rejection creates no job ID, job, channel, timer or
persistent caller accounting entry. No-worker `503` behavior remains unchanged.

| Environment variable | Default | Meaning |
| --- | ---: | --- |
| `DUMB_QUEUE_MAX_ACTIVE_JOBS` | 64 | Aggregate queued and leased jobs |
| `DUMB_QUEUE_MAX_JOBS` | 256 | Aggregate jobs including retained terminal jobs |
| `DUMB_QUEUE_MAX_BYTES` | 67108864 | Aggregate accounted payload and reservations |
| `DUMB_QUEUE_CALLER_MAX_ACTIVE_JOBS` | 16 | Active jobs for one caller |
| `DUMB_QUEUE_CALLER_MAX_JOBS` | 64 | All retained jobs for one caller |
| `DUMB_QUEUE_CALLER_MAX_BYTES` | 16777216 | Accounted payload and reservations for one caller |

Unset environment variables use the defaults. Explicit values must be positive
decimal integers that fit a Go `int`. Empty, zero, negative, fractional and
overflowing values fail startup. Active count limits cannot exceed their job
count limits, caller limits cannot exceed aggregate limits, and byte limits must
exceed the 1048576-byte result reservation. The embeddable server accepts
`Config.QueueLimits`; zero fields inherit defaults, negative or inconsistent
fields fail construction. `adminserver.QueueLimits` exposes the same type.

An active job accounts for UTF-8 bytes in its caller ID, model, prompt, system
and format, plus a 1048576-byte result reservation. Every admitted job reserves
its terminal job slot from the start. Completion cannot exhaust the result
budget after work has already been accepted. The same maximum applies to the
combined decoded output and error bytes. An oversized result fails without
completing or releasing the live lease; a valid replacement result can still be
submitted before its deadline. HTTP request bodies retain their existing
1048576-byte bound, including JSON encoding and lease metadata.

Done, failed and expired transitions release the active slot and unused result
reservation, stop the deadline timer, and discard prompt, system, format and
num_predict. The caller ID, model and actual output/error bytes remain accounted
until retention ends. Terminal jobs stay readable for one minute from completion
or the original deadline for an expired job. Retention releases the job slot and
remaining bytes and removes an empty caller accounting entry. Deadline and
retention timers plus opportunistic cleanup on admission, lookup, claim and
observation keep delayed timer scheduling from blocking available capacity.

These bounds describe logical retained payload and reservations, not process
RSS. Count limits also bound retained job metadata and job timers. HTTP decoding,
in-flight response/assignment copies, concurrent HTTP requests, Go runtime
allocation overhead and callbacks already executing remain outside this ledger.
No queue payload is logged by the new checks.

Verification:

```sh
go test -count=1 ./internal/queue ./internal/server ./cmd/dumb-server ./internal/protocol
go test -race -count=1 ./internal/queue ./internal/server ./cmd/dumb-server
go vet ./...
./qa-automation/scripts/build-gate.sh
npm --prefix clients/typescript run verify
```
