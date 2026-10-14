# Durable queue and idempotency verification

The first HTTP regression was run before implementation:

```sh
go test -count=1 -run TestIdempotentSubmitReturnsOriginalJobBeforeCapacityAndAvailability ./internal/server
```

It failed because two submissions with `Idempotency-Key: retry-key` received two
different job IDs and left two pending jobs. The same regression now passes,
including replay after the worker goes offline and a conflicting payload's 409.

`TestDurableHTTPProcessCrashBeforeResponse` starts a separate test process,
blocks its response writer before sending headers after durable submission,
kills the process without `Close`, waits for process death, then reopens the
same state directory. A caller retry without an online worker returns the
original job ID and deadline with one retained job. This establishes recovery
from a real process crash at the ambiguous-response boundary.

Queue regressions cover queued/leased/done/failed/expired recovery, persisted
caller ownership and payload conflicts, original deadline and retention, ack
deadline restoration and revocation, stale worker rejection, acknowledged lease
survival, heartbeat without disk writes, concurrent matching keys, caller scope,
retention key reuse, checksum/version/truncation corruption, oversized records,
symlinks, directory/file modes, exclusive process locks, known orphan cleanup,
unknown entries, disk saturation without poisoning and bounded temporary files.

Injected short-write, file-sync, rename and directory-sync errors fail acceptance
without applying an in-memory transition and poison later mutations/reads until
restart. Separate claim/ack/result fault regressions preserve the last
acknowledged state. Expiry fault tests preserve the prior lease on storage
failure and verify that successful expiry clears the stored prompt.

Independent review found that recalculating an active disk reservation on every
claim could reject already admitted work. The fix persists one allocation and
tests an exact-capacity directory through claim, ack, restart and a one-MiB NUL
result. Review also found a deadline crossing between validation and terminal
timestamp capture; a clock-advance regression now verifies one validated
completion timestamp survives restart. Race testing found whole-record
replacement racing with a waiter's owner/channel read; those values are now
captured under the queue mutex.

Commands:

```sh
go test -race -count=1 ./internal/queue ./internal/server ./cmd/dumb-server
go vet ./...
go test -count=1 ./...
./qa-automation/scripts/build-gate.sh
npm --prefix clients/typescript run verify
GOOS=windows GOARCH=amd64 go vet ./cmd/dumb-server ./internal/queue ./internal/server
```

Durable fixtures run on Linux/macOS. Windows durability is explicitly unsupported;
volatile Windows server compilation and existing worker behavior remain separate.
All cases use private temporary directories, isolated loopback HTTP and synthetic
credentials. No production server, installed worker, external Ollama, remote
repository or signed publication is touched.

Final results: all commands above passed. The core build gate is green; SDK
verification passed 26 unit tests, generated-contract drift checking and packed
independent Node/Fetch consumers. Windows cross-platform vet passed. Independent
read-only review found no remaining blocker and independently passed the durable
queue race subset. No application artifact was published or deployed.
