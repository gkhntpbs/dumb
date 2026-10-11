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
