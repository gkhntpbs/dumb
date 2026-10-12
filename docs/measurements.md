# Measurements

Dated runs of the measurement scripts. The numbers depend on the machine and on
the network path to Ollama, so every entry says what that was.

## Hedge budget — 2026-10-12

`QA_OLLAMA=1 ./qa-automation/scripts/hedge-budget.sh` — built `dumb-server` and
one `dumb-worker` (capacity 1) on 127.0.0.1, model `smollm2:135m`, 20 sequential
caller requests `POST /v1/jobs?wait=1500ms` with `"format":"json"`, after one
uncounted warm-up request. Latency is curl's `time_total` at the caller.

Machine: Apple M4, 16 GB, macOS 27.0.1, Go 1.26.5. **Ollama 0.35.0 is not on
this machine:** `127.0.0.1:11434` is a local proxy to Ollama on a Mac mini on
the LAN, so every number below includes that LAN hop (twice per job: the
request and the response).

| Run | `deadline_ms` | Done within 1.5 s | p50 | p95 | max |
|---|---|---|---|---|---|
| A | 10000 | 5/20 | 1503 ms | 1503 ms | 1503 ms |
| B | 10000 | 9/20 | 1502 ms | 1502 ms | 1503 ms |
| C | 1500 | 19/20 | 145 ms | 1414 ms | 1502 ms |

`503` (no worker for the model), 5 requests each:

| Case | median | max |
|---|---|---|
| No worker ever online | 0.4–0.5 ms | 1.1 ms |
| Right after the worker stopped (goodbye) | 0.4–0.6 ms | 0.8 ms |

What the runs show:

- A served job takes 70–400 ms end to end (worker log: 74–325 ms per job once
  the model is loaded; the first, cold job 949 ms).
- **One runaway generation blocks a capacity-1 worker until the job's
  deadline.** In runs A and B a single job did not finish (the small model kept
  generating in JSON mode) and held the only slot for the full 10 s deadline;
  every request behind it was still queued when its 1.5 s wait ran out, so it
  came back `202` and the caller would have taken its cloud path. The job's
  result was then refused (`409`, expired). With `deadline_ms` equal to the
  hedge budget (run C) the runaway is cut off at 1.5 s and the next jobs are
  served at once: 19/20 within budget, the one miss was the runaway itself
  (`410`).
- Consequence for callers that hedge: set `deadline_ms` to the hedge budget
  (the result is useless after it anyway), not to a generous value. Follow-ups
  are in `TODO.md` (generation cap, cancel a job the caller abandoned).
- The `503` path costs well under 2 ms, and after a goodbye it is immediate —
  no wait for the online timeout.

## Ack latency — 2026-10-12

`TestAckAddsNoLatency` (`internal/server`, loopback, fake worker): submit →
claim → ack → result → caller has the answer in 4.5 ms (limit 50 ms). The real
worker sends the ack in parallel with the generation, so it adds no time to the
job path at all; the launchd and dev runs served real jobs in 68–90 ms.

## Bounded generation and lease cancellation — 2026-10-12

`QA_OLLAMA=1 QA_DEADLINE_MS=10000 ./qa-automation/scripts/hedge-budget.sh`
ran against the same intentional `127.0.0.1:11434` proxy and
`smollm2:135m`, with one capacity-1 worker, an uncounted warm-up, and 20
sequential requests. Requests omitted `num_predict`; the server resolved it
to 128 and the worker passed it as Ollama `options.num_predict`.

| Deadline | Done within 1.5 s | p50 | p95 | max |
|---|---|---|---|---|
| 10000 ms | 20/20 | 154 ms | 338 ms | 434 ms |

All measured jobs returned HTTP 200 and `done`; worker generation/result
latencies were 85–433 ms. The warm-up took 502 ms. The no-worker 503 path
had median 0.4 ms / maximum 0.6 ms; after goodbye it had median 0.6 ms /
maximum 0.7 ms.

The contract accepts an optional `num_predict` from 1 to 128; omitted or
zero selects 128, and negative or greater values return 400. The fixed
server ceiling prevents the unlimited Ollama values from reaching a worker.
128 is a bounded starting limit for this repository's measured short JSON
workload, verified by the run above; it is not a general latency guarantee.
A capped completion may be truncated, including incomplete JSON. The job
still returns the generated text; callers must validate the output they need.

Workers retain their assignment deadline context and recheck the lease with
the existing idempotent ack endpoint every 250 ms while generating. This
uses four checks per second per running job, below the default worker rate
of 50 requests/s for the measured capacity of one. Lease-loss detection
also depends on request latency; each ack retains its existing 5 s timeout,
bounded by the job deadline. A transient ack failure is rechecked, and 409
cancels generation and drops the stale result. Queue ack and completion now
reject a past deadline even before the expiry timer runs.

Regression verification:

- `go test -count=1 -run 'TestGeneration|TestGenerateAlwaysBoundsTokens|TestGenerateCustomTokenLimit|TestGenerateDeadlineClosesRequest|TestExpiredLeaseRejectedBeforeTimer' ./internal/worker ./internal/ollama ./internal/queue ./internal/server`
  passes with the implementation.
- The new regression files were copied into an isolated `git archive 08c6782`
  baseline from before this change and run with
  `go test -count=1 -run 'TestGeneration|TestGenerateAlwaysBoundsTokens|TestGenerateCustomTokenLimit|TestExpiredLeaseRejectedBeforeTimer' ./internal/worker ./internal/ollama ./internal/queue ./internal/server`.
  Exit status was 1. `TestGenerationStopsAfterLeaseLoss` reported generation
  continuing after lease loss; `TestGenerationUsesAssignmentLimitAndDeadline`
  reported the missing generation limit; `TestGenerateAlwaysBoundsTokens`
  reported a request with no options; `TestExpiredLeaseRejectedBeforeTimer`
  rejected neither an expired ack nor completion;
  `TestGenerationLimitValidationAndAssignment` found missing assignment limits
  and negative/above-ceiling values accepted as 202.
  `TestGenerateCustomTokenLimit` reported zero instead of 17 and accepted -2.
- `TestGenerateDeadlineClosesRequest` verifies the actual Ollama HTTP client's
  connection is canceled at the deadline, using a local HTTP fixture that waits
  for its request context to close. Deadline cancellation already existed;
  this test protects it. Lease-loss cancellation is covered by a blocking
  generator and an ack endpoint returning 204 then 409.
- `npx @redocly/cli lint docs/protocol/openapi.yaml` (CLI 2.62.1) reports a valid
  description with zero warnings; `go test -run TestTypesMatchSpec ./internal/protocol`
  and `./qa-automation/scripts/build-gate.sh` pass.

The live hedge run proves the bounded path meets the requested budget on
this proxy/model. It did not force a deadline or revoked lease upstream;
the proxy's propagation of a canceled client connection to its LAN Ollama
request and the remote model's stopping behavior remain unmeasured.

## Hermetic Ollama recovery lab — 2026-10-12

`./qa-automation/scripts/recovery-lab.sh` compiles three test binaries serially,
then runs fresh `^TestRecovery` cases with `GOMAXPROCS=2`,
`GOMEMLIMIT=256MiB`, `GOFLAGS=-p=1`, `-count=1`, `-parallel=1` and a 30 s
per-binary timeout. `QA_OLLAMA=0` and `QA_LAUNCHD=0` prevent real opt-in tests.
There are no VM/container images, official installers or model downloads.
The intentional real 127.0.0.1:11434 proxy is never contacted.

Measured with macOS `/usr/bin/time -l` on the same Apple M4 / 16 GB host:

| Dedicated test process | Maximum RSS | Wall time | Swaps |
|---|---|---|---|
| `recoverylab.test` | 17,973,248 bytes (17.1 MiB) | 0.86 s | 0 |
| `ollama.test` | 12,075,008 bytes (11.5 MiB) | 0.29 s | 0 |
| `worker.test` | 16,384,000 bytes (15.6 MiB) | 0.46 s | 0 |

The three compiled test binaries and build scratch occupied 31,668 KiB
(30.9 MiB), measured with `du -sk`. The script removed only its created
scratch directory on exit. Fixture file/symlink content after individual
scenarios was 0–55 bytes; the instrumented peak was 263 bytes. These content
counts exclude directory metadata, filesystem allocation blocks, binaries
and the existing shared Go cache. The 64 KiB per-fixture content assertion is
a test safeguard, not an OS disk quota. `GOMEMLIMIT` is a soft runtime target;
compiler and full-gate Node processes are separate from these RSS measurements.

The fresh dedicated run passed 49 leaf cases: 30 lab scenarios, 12 Ollama
safety/regression cases and seven worker cases. Five further CLI provenance/
setup leaf cases passed in the normal package suite. Coverage includes 20
base fault scenarios, six owned API/tag fault variants, failed-cycle cooldown,
cancellation, boundary refusal and root cleanup; real in-memory DUMB queue
jobs complete after startup repair and after generation-500 restart while
version/tags remain healthy. Configured and previously reachable implicit
proxies are not installed over or restarted.

Behavioral red-before-fix evidence:

- On the pre-change `d8c66d7` implementation,
  `TestRecoveryStartsMacCLIWithoutGUI` failed with `runs=[open -a Ollama]`
  and no CLI start; `TestRecoveryWithdrawsUnavailableModels` failed because
  unavailable models stayed advertised. Both now pass.
- New safety tests initially failed for accepted raw parent archive traversal,
  launch after cancellation, and starting two owned processes while replacing
  the tracked handle. They now pass alongside symlink/duplicate/HTTPS-downgrade
  and existing-file protections.
- `TestRecoveryLabImplicitProxyIsRemembered` initially failed with one
  simulated download/extract/start after loss of a healthy implicit proxy.
  Successful model-list observation now records external ownership before
  loss; the same test passes with no process/install operations.

Two lab-only issues were found and corrected: hanging fake POST pulls left
unread bodies and prevented server-side cancellation observation during
cleanup, so the fixture drains bodies and has an explicit scoped shutdown
signal; direct test binaries inherit `GOTMPDIR` paths containing repeated
slashes, while `filepath.Join` cleaned the candidate install path, so the
root prefix check rejected its own fixture. Diagnostic output proved the
raw `T//...` versus cleaned `T/...` mismatch; fixture roots are canonicalized
before guard comparison without loosening `os.Root` confinement.

The final full gate passed with
`GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 QA_OLLAMA=0 QA_LAUNCHD=0 ./qa-automation/scripts/build-gate.sh`.
Read the supported recovery envelope and remaining native-install/orphan/
OS-notification gaps in `ollama-recovery.md`. The fixtures execute no actual
installer, daemon or model; passing them does not verify native macOS focus,
Gatekeeper or process-tree cleanup behavior. No real installation or proxy
configuration was modified.

## 2026-10-12 — macOS menu helper and graceful controls

Implemented a native AppKit accessory helper with protocol-v1 stdio and a local
application bundle. No host Ollama, model download, installer, launchd install,
proxy on port 11434 or persistent application was touched during these tests.
The queue and Ollama API used the sealed recovery lab and random loopback ports;
credentials were generated at runtime. Windows remains deferred.

Commands:

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB ./scripts/build-macos-app.sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 ./qa-automation/scripts/build-gate.sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB go vet -p 1 ./cmd/dumb-worker ./internal/menubar ./internal/worker ./internal/service
QA_MENU_AUTOMATED=1 \
QA_MENU_BAR_HELPER="$PWD/dist/DUMB Worker.app/Contents/Resources/DUMBMenu.app/Contents/MacOS/dumb-menu" \
QA_MENU_DIAGNOSTICS="$PWD/dist/menu-automated.jsonl" \
GOMAXPROCS=2 GOMEMLIMIT=256MiB /usr/bin/time -l go test -p 1 -count=1 -v ./cmd/dumb-worker -run '^TestMenuNativePreview$'
```

The final finite native run passed in 1.32 s (timed Go command 2.92 s). The timed
invocation reported maximum RSS 164,544,512 bytes (156.9 MiB), zero swaps and peak
memory footprint 18,547,360 bytes. These are `/usr/bin/time -l` process-accounting
values for the compile/test invocation, not a combined Go/AppKit hard memory
limit. `GOMEMLIMIT` is a soft Go heap limit and does not limit native AppKit.
The local bundle occupied 9,384 KiB; no large assets or additional model files
were created. Owned local output was rebuilt successfully through staged swaps.

The native helper consumed short JSON updates while stdin remained open and
reported recovery, ready, serving, error, updating, draining, paused and stopping.
Actual diagnostics are retained in `docs/qa/menu-bar-2026-10-12.jsonl`: zero
content windows, accessory activation policy, `isActive=false` and unchanged
foreground focus throughout. Framework `NSStatusBarWindow` surfaces are counted
honestly (1 initially, up to 9 in AppKit's status-bar surface pool). A status-bar
surface is required for the icon and is not a content window. The maximum active
job count was 1. The fake backend recovered once; the queue completed jobs before
and after pause/resume, and Quit drained without a duplicate worker or process.
In this finite run controls were sent through a fixture channel, not clicked in
the native menu. Error/update display phases were injected, not real downloads.

The new default-gate cases cover menu lifecycle/control priority, helper death
and missing/mismatched/timed-out handshake, explicit headless opt-in, fatal
credential controls, upgrade backoff/retry, rollback restart preservation and
ordered active-job snapshots. The native offline self-test validates 45 valid
state/category combinations and 10 malformed messages before any application
or workspace access. The existing updater suite and cross-platform Go tests
remain green. The macOS CI job compiles/tests the helper and builds the local
application; remote CI was not run.

Red-first evidence before the associated fixes:

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB go test -p 1 -count=1 ./cmd/dumb-worker ./internal/worker -run 'TestMenuQuitSupersedes|TestMenuAlreadyDead|TestStatusDelivery'
```

All three cases failed: `quit was discarded during drain`, `worker started
without menu control`, and `stale active count delivered after completion`.
They passed after allowing Quit to supersede Pause, checking UI health before
worker startup and serializing snapshot capture/delivery. An initial real
native preview also exposed a short-line pipe buffering defect: no status
updates/diagnostics arrived while the helper used `readData(ofLength: 1024)`.
Replacing it with `availableData` made live updates immediate; the finite
native test now asserts required short-line display states and reads only the
current invocation's diagnostics.

Visual acceptance remains incomplete. The enabled computer-use tool could read
a normal running application, but attaching the windowless helper and
SystemUIServer returned timeout `-10005`. Its macOS API also rejected direct
window-ID selection; the helper's AppKit status-button window number was
4294967296. Manual preview attempts reached their bounded 180-second timeout
without menu clicks. No actual menu screenshot or real menu-click acceptance
was obtained, and no substitute image is presented as a screenshot. Diagnostic
and fixture-channel evidence is separate from that remaining UI check.

Native signed/notarized distribution and GoReleaser application packaging,
quarantined-release behavior, actual installer silence and macOS-owned BTM
notifications remain unverified. The existing Go updater never modifies the
native helper. No push, publication or deployment occurred.


## 2026-10-12 — Uncoil sealed native menu preview

The opt-in `scripts/menu-preview.sh` launcher compiles the sealed recovery/queue
fixture and runs the actual AppKit helper, without host Ollama, model downloads,
installers or service changes. Interactive polling uses a 20-second server wait
and two-second model checks. Runtime duration is bounded to eight hours; Go
compilation is serial with `GOMAXPROCS=2` and `GOMEMLIMIT=256MiB`.

The managed Uncoil Start completed in 1.1 seconds and reached the exact
`DUMB menu preview ready` marker with running status. SendInput `pause` produced
draining then paused; `resume` produced starting. Injected `error` and `ready`
display commands were acknowledged. Managed Stop completed in 0.2 seconds,
returned idle, and a process-list check found no remaining preview test or
native helper processes. A second Start followed by SendInput `quit` exited
with code 0 and `PASS`; no preview/helper processes remained. Restart then
started a fresh running preview. Restart of an already running preview completed
in 0.6 seconds; the previous PID was gone and exactly one new preview and
helper remained. The final preview was left running for manual inspection.
These are terminal controls and process lifecycle
evidence, not actual native menu clicks or screenshots.

Verification:

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 GOPROXY=off GOSUMDB=off go test -count=1 ./cmd/dumb-worker ./internal/menubar
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 GOPROXY=off GOSUMDB=off ./qa-automation/scripts/build-gate.sh
bash -n scripts/menu-preview.sh
git diff --check
```

All passed. The duration regression covers the default, minimum, eight-hour
maximum and rejected invalid/out-of-range values. Default gate tests remain
noninteractive; no new native preview ran concurrently with the managed run.
Actual menu-click/screenshot acceptance, signed native distribution, clean
installer behavior and macOS-owned BTM notifications remain pending.
