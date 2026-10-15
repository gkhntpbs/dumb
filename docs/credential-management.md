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

## Admin panel: Add worker and Re-pair

The admin panel creates pairing codes itself; no private script is needed. The
Devices view has an **Add worker** button and each worker's details dialog a
**Re-pair worker** action, which sends that `worker_id` so the identity is kept
(use it before the current credential expires; the old credential stops working
when the new code is exchanged). The dialog takes an optional display name (it
only fills `-name` in the generated commands; nothing is stored server-side),
the models, the code lifetime (2, 5 or 10 minutes, default 10) and the
credential lifetime (7, 30, 90, 180 days or 1 year, default 1 year).

**Generate** calls `POST /admin/v1/pairing` once (repeated clicks cannot create
a second code). The result shows the code with a Copy button, a countdown to
`expires_at` and an expired state, the worker ID, and copy-able Linux, macOS and
Windows commands that read the code from a hidden prompt, never from the command
line. The plaintext code exists only in memory and one text node while the
dialog is open; closing it (Close or Escape), expiry, sign-out or session loss
erases it. It is never written to browser storage or logs.

The commands use `public_url` from `GET /admin/v1/session` (server
`Config.PublicURL`, reference server `DUMB_PUBLIC_URL`), not the page origin,
because the panel is reached through an SSH tunnel. Without it the dialog asks
for the server URL (https only) and remembers only that URL in the browser.

The session endpoint also reports the session `role` and
`credential_management`. Without management configured, **Add worker** opens an
explanation instead of failing on the strict Origin check; an `admin-read`
session sees a disabled button. Management requests still need the exact
configured `AdminOrigin`, so open the panel at that origin (scheme, host and
port of the tunnel). Errors (401, 403, 404, 409, 429, 503, timeouts) are shown
as plain messages. Acceptance: [qa/add-worker-2026-10-15.md](qa/add-worker-2026-10-15.md).

The native setup screens are unchanged: the Windows first-run setup still asks
for a worker token, so the generated Windows commands pair first and hand the
resulting token to that setup. Actual registry, migration and production
enablement belong in the private embedding application, not this repository.
