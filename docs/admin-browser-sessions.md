# Admin browser sessions

The admin page and `/admin/v1/` API share one origin. The browser requests
`GET /admin/v1/session`, signs in with `POST /admin/v1/login` and a JSON
`password`, and signs out with `POST /admin/v1/logout`. No server address or
bearer credential is entered in the page. API paths are generated from OpenAPI
into `internal/server/adminui/assets/admin-contract.js`.

The public `adminserver` package exposes `New(Config)` and the
`SessionProvider` boundary. An embedding application supplies password
verification, session expiry, cookie issuance and revocation. `Authenticate`
returns an `admin-read` principal; caller and worker identities cannot read admin
metadata. Configuring a browser provider disables the bearer fallback on the
admin handler. Without a provider, session inspection reports false and login
and logout return 503. Existing bearer metadata clients remain supported only
when no browser provider is configured.

An integrated private executable creates one Server, serves its `ServeHTTP`
handler for caller/worker traffic and its `AdminHandler()` behind a separate
loopback listener. This ensures observations read the live queue rather than a
second server. `ListenAdmin` preserves the numeric-loopback guard. The feature
remains off by default in the reference public server.

All admin responses use `Cache-Control: no-store`. Login/logout require an
Origin matching the request Host, and cross-site fetches are denied. Forwarded
host/protocol/address headers never satisfy the origin check. TLS-terminating
proxies must preserve the public Host. The private provider additionally requires HTTPS and an Origin matching the
actual request scheme and Host, and issues Secure, HttpOnly, SameSite cookies.
The integrated executable terminates TLS itself; forwarded TLS headers are not
trusted. Passwords, production origins and session storage belong in
the private repository. No production password implementation is shipped here.

Verification:

```sh
go test ./internal/server ./adminserver
npm --prefix clients/typescript run check:contract
```

The API page reads `GET /admin/v1/api` from the same Server. It observes all
completed public-handler requests, including authorization failures, rate limits,
unknown routes and errors. Admin requests do not affect these counters. Totals
reset at the server epoch; outcome counts and average HTTP latency describe a
bounded ring of thirty ten-second completion buckets (the current bucket is
partial). History starts at the server startup bucket, so new processes do not
present unobserved earlier traffic as zero. Request rate divides the completed
window count by the actual observed duration, capped by process uptime.

Inflight requests are shown separately. HTTP latency includes caller waits and
worker long polls; it does not measure model inference latency. `serving` means
the handler can return this observation, not that workers or models are ready.
This is operational HTTP traffic, not token usage or billing. Metadata consists
only of counters and fixed route templates, with one `other` label for unknown
paths; request bodies, query strings, identities, credentials and IP addresses
are never retained. Memory usage is independent of request volume or path
cardinality. The five-minute history is process-local and is not durable.

Verify API traffic collection and concurrency with:

```sh
go test -race ./internal/server -run 'TestAdminApi|TestApiMetrics'
```
