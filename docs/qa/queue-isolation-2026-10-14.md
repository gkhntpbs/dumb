# Queue admission and caller isolation verification

Before the ownership check was enforced,
`go test -count=1 -run TestCallerOwnershipQueuedJob ./internal/server` failed:
another caller's `wait=10s` lookup returned `410` with the job ID and expired
state after 10 seconds, where an immediate `404` was required.

With ownership checks absent, the expanded regression command
`go test -count=1 -run TestCallerOwnershipEveryLifecycleState ./internal/server`
failed all five states. Queued and leased jobs returned `202` after a one-second
wait; done and failed jobs returned `200` with private output or error; expired
jobs returned `410` after a one-second wait.

The final tests cover all five states, unknown-job response equivalence, immediate
rejection of another caller's wait, principal-preserving credential replacement,
wrong roles and invalid credentials, and ignored caller-supplied owner fields.

Queue tests cover concurrent caller and aggregate count boundaries, exact UTF-8
byte admission, rejection without stored allocation, caller-before-global
precedence, terminal retained counts, result reservation, oversized-result lease
preservation, expiry releasing lease capacity, one-minute retention releasing
payload and caller accounting, and invalid startup configuration. All fixtures
are isolated and use small payloads except the explicit one-MiB result boundary.
No production server, worker installation or external Ollama is contacted.

The admission HTTP regression was also run against a disposable archive of the
pre-change committed queue. Only an inert `Limits` type and a `Config.QueueLimits`
field were supplied so the new test could compile; that queue implementation was
unchanged. `TestAdmissionHTTPCallerAndGlobalCapacity` failed because a saturated
caller received `202` with a newly allocated job rather than `429`. The scratch
copy was deleted without modifying the working tree or its branding changes.

Final validation passed:

- `go vet ./...`
- `go test -count=1 ./...`, including generated-contract drift and real HTTP SDK integration
- `go test -race -count=1 ./internal/queue ./internal/server ./cmd/dumb-server`
- `./qa-automation/scripts/build-gate.sh`
- `npm --prefix clients/typescript run verify`: 23 caller tests and packed independent consumers
- `npx --offline @redocly/cli lint docs/protocol/openapi.yaml`: valid, with the existing unused `AdminApiCounters` component warning; the component already exists without references in the pre-change contract

The final build gate also passed after the additional concurrent UTF-8 byte
boundary and late deadline/retention observation regressions were added.

The generated caller contract includes the optional positive-seconds
`Retry-After` header on `503`; request and result shapes are unchanged. The SDK
uses consumer-owned fallback once on global saturation and exposes caller
saturation without automatic retries or implicit fallback. No build, deploy,
push or release was performed for this change.
