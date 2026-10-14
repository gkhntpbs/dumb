# Credential management boundary

Credential management is an optional private provider exposed through the public
`adminserver` embedding facade. Configure `Credentials`, `AdminSessions` and an
exact `AdminOrigin` to enable management. The standalone reference server has no
production registry. Providers must make each mutation durable before returning
success and must keep plaintext credentials out of their persisted state.

The explicit `admin-manage` session role grants management and existing admin
read access. It cannot call worker/caller APIs. Existing `admin-read` sessions
cannot issue, rotate, revoke or list credentials. Management never falls back to
bearer authentication. Every management request, including inventory GET, must
supply the exact configured scheme and host in its `Origin` header; JSON objects
are required for mutations. Responses carry `Cache-Control: no-store`.

The contract defines these private-listener endpoints:

- `GET /admin/v1/credentials?offset=0&limit=50`: current metadata inventory;
  limit 1–100, offset 0–10000. Includes no token, code or hash and is not an audit
  log or a stable pagination snapshot.
- `POST /admin/v1/credentials`: `role` is `caller` or `worker`, with required
  `ttl_seconds` from 1 to 31622400. Returns a new stable principal and credential.
- `POST /admin/v1/credentials/{id}/rotate`: required `ttl_seconds` in the same
  range. Replacement preserves principal identity and role; old token retires
  atomically. The ID targets a credential, not a principal.
- `POST /admin/v1/credentials/{id}/revoke`: body `{}`. Revokes immediately;
  repeated revoke of a retained credential is successful.
- `POST /admin/v1/pairing`: required code `ttl_seconds` from 1 to 600 and
  `credential_ttl_seconds` from 1 to 31622400; optional `worker_id` pairs an
  existing worker principal. A caller principal cannot become a worker.

Tokens and codes are 32 random bytes encoded as 43-character base64url strings.
Only issue/rotate/exchange responses return plaintext tokens; only code creation
returns a plaintext code. Successful exchange, not code creation, retires prior
worker credentials. A lost secret response cannot be replayed from hash-only
storage: issue a new code or rotate the inventory credential explicitly.

`POST /v1/workers/pair` is a public, unauthenticated JSON exchange endpoint with
mandatory worker-version floor. Its request body is capped at 1024 bytes. Its
separate per-IP attempt bucket defaults to 0.5/s, burst 10, with at most 4096
keys. Overflow requests receive 429 without allocating another key. Pairing
attempt exhaustion does not block already authenticated workers behind that IP.
See [worker-pairing.md](worker-pairing.md) for the HTTPS-only CLI.

A worker bearer is authenticated again immediately before delivering a claimed
assignment. Revoke, rotation, expiry, changed identity or changed role denies the
assignment and durably releases its undelivered lease. A release storage failure
returns sanitized 503 and leaves the poisoned queue fail-closed. Already
accepted leases retain their result/deadline semantics; token management does
not silently cancel them. Worker presence can remain visible until its ordinary
freshness timeout, and credential changes do not restart worker processes.

The existing read-only admin UI has no credential-management controls. These
endpoints and the explicit CLI provide the workflow without changing native
setup screens. Actual registry, migration and production enablement belong in
the private embedding application, not this repository.
