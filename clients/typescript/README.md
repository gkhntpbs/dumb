# DUMB caller

A reusable TypeScript caller for Distributed User-owned Model Backend. The ESM
package uses standard Fetch, AbortController and web APIs, with no runtime
dependencies or application/provider coupling. Node.js 22+ and runtimes with
these web APIs are supported. This package is private to prevent accidental
publication; local installation from its tarball is supported with
`npm install /path/to/dumb-caller-0.1.0.tgz` in a consumer project.

## Build and verify

From this directory:

```sh
npm ci
npm run verify
npm pack
```

`verify` checks contract regeneration, compiles the package, runs unit tests,
builds/tests an isolated source checkout with offline locked dependencies,
and installs the tarball into an isolated consumer directory. Both example
consumers compile and run against the installed package without this workspace
or Go. `npm run build` works from a standalone source checkout with its locked
dev dependencies installed. Normal package use requires only the shipped
`dist` files and standard web APIs.

`src/contract.ts` is generated from `../../docs/protocol/openapi.yaml`, the sole
contract authority. `npm run generate` updates it; `npm run check:contract`
fails on drift. The generator strips comments and also derives deadline/token
limits from the schema. Generation/checking requires the core contract;
compilation and consumption do not. No contract copy is maintained here.

The core gate discovers `TestTypeScriptCaller` through `go test ./...`; it reads
package sources and the contract so their changes invalidate Go test caching. Install
this package's dev dependencies before running the gate. That test also runs
the caller against the actual Go DUMB HTTP server with local worker fixtures, including
a job remaining claimable after the client hedges.
No Ollama instance or production credentials are needed. CI installs the
locked tooling first. The fixture generates credentials at runtime and passes
them to the client through `QA_` environment variables.

## Use

```ts
import { DUMBClient } from "@dumb/caller";

const client = new DUMBClient({ baseURL, token });
const result = await client.execute(
  { model, prompt, deadline_ms: 1500, num_predict: 64 },
  { hedgeMs: 1000, signal, fallback: async (input, context) => fallback(input, context.signal) },
);
const output = result.source === "dumb" ? result.output : result.value;
```

Applications supply `baseURL`, `token`, prompts and their own fallback function.
HTTPS is required except for loopback HTTP; redirects are refused to avoid
forwarding credentials. An optional injected `fetch` supports other runtimes
and tests. `requestId` is sent as `X-Request-ID`.

`submit(input, { waitMs, signal })` calls `POST /v1/jobs` once. `getJob(id,
{ waitMs, signal })` calls `GET /v1/jobs/{id}`; `status({ signal })` calls
`GET /v1/status`. Wait values are milliseconds, encoded as Go duration strings.
Low-level methods return queued, leased, done, failed, expired or cancelled job
statuses; HTTP 410 is a normal expired or cancelled job response. `cancel(id,
{ signal })` calls `POST /v1/jobs/{id}/cancel` once: it returns the cancelled
status (also when repeated) or, with HTTP 409, the unchanged done, failed or
expired status; a result committed first is never discarded. Only the caller
that submitted the job can cancel it; others receive 404.

`execute` submits once and polls that job until it finishes or the configured
budget expires. Its budget is the smaller of `deadline_ms` and optional
`hedgeMs`. A done job returns `{ source: "dumb", output, job }`. An injected
fallback runs for no-worker HTTP 503 immediately, failed/expired jobs, or an
elapsed local hedge/deadline budget, returning `{ source: "fallback", value,
reason }`. Without a fallback these conditions throw `JobError`. The fallback
receives the original input, reason, any known job status, optional request id
and the consumer's signal. Its own timeout policy belongs to the application.

External abort cancels fetch and propagates without fallback. An abort during
fallback rejects the caller even if the callback ignores its signal; the
callback must honor that signal to stop its own work. HTTP errors
other than 503 throw `HTTPError` with status, request id and Retry-After;
malformed successful responses throw `ProtocolError`. Network errors propagate.
No HTTP request is automatically retried: retrying a submission after losing
its response can create duplicate jobs.

By default a local hedge or abort stops the client's waiting request, not the
server job: it remains eligible until its submitted deadline, and no hidden
request is sent. With `cancelOnAbandon: true`, execute sends one best-effort
cancel (no retry, errors ignored, fallback not delayed) when it stops waiting
for a job that is still queued or leased. If the hedge fires while the
submission is still in flight, that submission is allowed to finish (only the
consumer signal aborts it) and the job is cancelled once its id arrives. A
consumer abort during submission loses the id; to cancel such an ambiguous
submission, repeat `submit` with the same `idempotencyKey` (it returns the
original job while it is retained) and call `cancel`. After retention ends
the same key creates a new job. A job seen as `cancelled` while polling ends
execute with `JobError` reason `cancelled`; the fallback is not run, because
the application already abandoned that work. A cancelled leased job stops
generating on its worker at the next lease check (about 250 ms). Set
`deadline_ms` to the actual usefulness budget. `num_predict` follows the server's contract ceiling and may truncate
output, including JSON; consumers must validate output before using it.

`examples/node-batch.ts` consumes completions in Node; `examples/fetch-handler.ts`
shows a Request/Response integration with an injected fallback. Neither names
an application or provider. Credentials and private server addresses are
supplied at runtime, never committed.

## Local verification — 2026-10-12

The core build gate passed with 21 caller unit tests, the actual Go-server
fixture, generated-contract checking, standalone source compilation/tests,
and tarball-installed consumption by both examples. Both consumer examples
were executed in Node.js 26.0.0, including its standard Request/Response APIs;
a separate browser or hosted Fetch runtime was not executed. Remote CI and
publication were not run.

A deliberate extra generated type made `npm run check:contract` fail with exit
1. A normal `go test ./clients/typescript` pass was followed by a cached pass;
adding a deliberately failing JavaScript assertion then made the same Go
command fail with exit 1. Both mutations were restored before the final gate.
This verifies the gate observes TypeScript test edits instead of reusing a
stale Go subprocess result.

## Explicit idempotent retry

Pass a consumer-owned `idempotencyKey` to `submit` or `execute` when resolving an
ambiguous accepted POST. The same caller, key and normalized payload return the
original job before online-worker or admission checks; a payload conflict is
HTTP 409. Keys match the generated contract pattern and remain available until
one minute after the job finishes or expires. Reusing a key after retention
creates new work. Durable servers preserve the mapping across process restart;
explicit volatile servers do not. The client never generates keys or retries
implicitly. See `../../docs/durable-queue.md` for storage and recovery semantics.
