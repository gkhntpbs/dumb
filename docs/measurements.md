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
