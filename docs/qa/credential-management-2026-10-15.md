# Credential management acceptance — 2026-10-15

The contract changed first in `docs/protocol/openapi.yaml`. The first new targeted
suite failed to compile because the credential DTOs/provider were absent:
`protocol.CredentialPage`, `IssueCredentialRequest`, `IssuedCredential`,
`CreatePairingRequest` and `PairingCode` were undefined. This is evidence of an
absent feature, not a claimed behavioral reproduction against an old release.
The CLI separately recorded a red build before `cmdPair` existed.

Implemented evidence is in `internal/server/credentials_test.go`,
`internal/protocol/protocol_test.go` and `cmd/dumb-worker/pair_test.go`:

- Cookie-only manage role, exact Origin including GET, read/worker role denial,
  invalid/missing TTL, unknown fields, non-object JSON and sanitized backend
  failures; manage sessions retain existing read access.
- Separate version-checked public exchange, JSON and body bounds, attempt rate
  and map bounds; pairing exhaustion leaves valid worker authentication intact.
- Revoke, same-principal rotation, expiry, identity change and role change after
  claim deny delivery and release the durable lease. Close/Open confirms one
  queued job and zero old leases. A separate HTTP test parks a long-poll before
  revoke and then submits a job, confirming denial and immediate release.
- TLS CLI dispatch, hidden pipe input, no arguments/environment code, no echo,
  version header, no redirects/retries, bounded responses/errors, restrictive
  atomic token replacement and unchanged configuration/startup.

Passed locally:

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 QA_OLLAMA=0 QA_LAUNCHD=0 ./qa-automation/scripts/build-gate.sh
GOMAXPROCS=2 GOFLAGS=-p=1 go test -race -count=1 ./internal/server ./internal/queue ./cmd/dumb-worker
GOMAXPROCS=2 GOFLAGS=-p=1 go vet ./...
npx --offline @redocly/cli lint docs/protocol/openapi.yaml
npm --prefix clients/typescript run verify
```

The gate includes generated TypeScript contract drift, SDK build/unit/packed
consumer/server integration and the admin authorization/schema/panel checks.
No native UI changed, so no new screenshot acceptance is claimed. Actual Windows
console/ACL execution is deferred at the user's request; cross-compilation does
not establish that acceptance. No production registry activation, push, deploy
or signed release is part of this local stage.

The first OpenAPI lint found two unquoted comma descriptions interpreted as YAML
flow-map keys; these were quoted and regenerated. Final lint is valid with the
pre-existing unused `AdminApiCounters` warning. Independent review found no
blocking management boundary/claim issue; its non-object revoke observation was
covered by strict JSON-object validation and an explicit regression. CLI review
added current-user parent ownership and trusted/protected ancestor checks with
ownership-policy and actual writable/sticky-directory tests.

Windows amd64 cross-vet passes for `./internal/server ./internal/queue`.
Adding `./cmd/dumb-worker` to that vet run fails the existing
`setup_console_windows.go:56:55` unsafe.Pointer warning. Worker cross-build
passes with `GOOS=windows GOARCH=amd64 go build -o /dev/null ./cmd/dumb-worker`;
this is compile evidence only and does not replace native Windows acceptance.
