# Caller cancellation acceptance — 2026-10-15

The contract changed first in `docs/protocol/openapi.yaml`: `POST /v1/jobs/{id}/cancel`,
the terminal `cancelled` job state (410 on read and idempotent replay), cancellation
wording on ack/result 409, and the admin `cancelled` counter and state filter. The
TypeScript contract was regenerated with `npm --prefix clients/typescript run generate`.
The first queue suite failed to compile because `Queue.Cancel`, `ErrFinished`,
`protocol.StateCancelled` and `Observation.Cancelled` did not exist — evidence of an
absent feature, not a behavioral reproduction against an old release.

## Semantics

- Only the submitting caller principal can cancel. Unknown jobs, other callers and
  ended retention get the same 404 body. Worker, admin-read and admin-manage
  credentials get 401.
- Queued: leaves the queue at once and is never dispatched. Leased: the lease ends at
  once, the worker slot is freed (a parked claim wakes), ack and result get 409 and the
  result is discarded. Waiting callers wake with `cancelled`.
- Repeat cancel returns 200 with the same status. A job already done, failed or expired
  returns 409 with its unchanged status; a committed result is never discarded.
- Races: one mutex, first durable commit wins. At or after the deadline the job is
  expired first, so cancel returns 409 `expired`. A cancel committed before the
  deadline is not overwritten by the deadline timer. Unacked-lease revocation and
  undelivered-claim release never requeue a cancelled job.
- Durability: the cancelled record is written atomically before the response. Restart
  restores it as terminal with the same retention, its Idempotency-Key replays the same
  job, no lease is restored and the job is not dispatched. A failed write is neither
  published nor acknowledged and poisons the queue as for other mutations.
- Worker propagation uses the existing 250 ms ack lease check; no protocol change for
  workers. A closed caller connection is never treated as cancellation.
- SDK: `cancel(id)` sends one POST (200/409 return `JobStatus`, others `HTTPError`).
  `execute` is unchanged by default. Opt-in `cancelOnAbandon` sends one best-effort
  cancel for a known active job on hedge, budget end, consumer abort or polling error;
  if the hedge fires during submission, the submission finishes (only the consumer
  signal aborts it) and the job is cancelled when its id arrives. A job observed as
  `cancelled` ends `execute` with `JobError("cancelled")` and no fallback.

## Tests

- `internal/queue/cancel_test.go`: queued, leased (ack/result/release/revoke refused),
  slot wake-up, repeat, after done/failed/expired, ownership, deadline before/after,
  200 concurrent cancel/complete races with exactly one winner, durable restart for
  queued and leased cancellation, injected rename failure not published.
- `internal/server/cancel_test.go`: HTTP queued/leased/repeat, waiting caller woken,
  after complete (409 + output kept), after deadline (409 expired), other caller 404
  identical to unknown, worker/admin-read/admin-manage/missing/invalid tokens 401,
  durable server restart, admin counter and filter.
- `internal/worker/cancel_test.go`: real server and worker; a caller cancel stops the
  in-flight generation (measured 200–203 ms after cancel in three `-race` runs), no
  result is posted and the freed slot completes the next job.
- `clients/typescript/test/client.test.mjs` (33 unit tests, 7 new) and
  `test/integration.test.mjs` against the real Go server: cancel queued/leased,
  repeat, keyed replay returns `cancelled`, after completion, opt-in abandon after a
  5 ms hedge leaves nothing to claim, 401 for a worker token, 404 for unknown.
  Mutation check: setting `cancelOnAbandon: false` in the integration test made
  `go test ./clients/typescript` fail; restored.
- Admin panel test treats `cancelled` as terminal (no countdown/age).

## Commands (passed locally, go1.26.5 darwin/arm64, Node.js 22.23.2)

```sh
go vet ./...
./qa-automation/scripts/build-gate.sh            # GATE GREEN
go test -race -count=1 ./internal/queue ./internal/server/... ./internal/worker ./internal/protocol ./clients/typescript
npm --prefix clients/typescript run verify       # contract drift, build, 33 unit tests, package
GOOS=windows GOARCH=amd64 go build ./... && GOOS=linux go build ./...
```

`go test -race -count=1 ./...` passed every package except `internal/menubar`
(`TestSessionFailsClosed/die: death not detected`), which also fails under `-race`
with these changes stashed; it passes in the non-race gate. It is pre-existing and
unrelated.

## Not done

- No push, release or deployment; production still runs the earlier binary.
- Legacy workers that do not check their lease keep generating until done; their
  result is refused with 409.
- No Windows run; CI's Node.js 26 was not used locally (22.23.2 was).
- Admin panel change is text/terminal-state logic and one filter option; no new
  layout. No screenshot was taken.
