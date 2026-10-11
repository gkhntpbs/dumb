# AGENTS.md — DUMB core

DUMB (Distributed User-owned Model Backend): the protocol, the server (`cmd/dumb-server`), the
worker (`cmd/dumb-worker`) and the `Auth` interface, in Go. **Status:** protocol v1 is defined; the server
has an in-memory queue with leases (capacity enforced), long-poll and caller endpoints, a static-token
`Auth`, per-principal / per-IP rate limits and `log/slog` logging with request ids.
The worker runs the agent loop (hello with the configured models Ollama has, re-checked every
minute; long-poll; generate on the local Ollama), has `doctor`,
`setup` and `service` commands, and updates itself from signed GitHub Releases (minisign,
atomic swap, probation + rollback, `stable` / `beta` channels). Release pipeline: goreleaser +
GitHub Actions, not yet run for real. Next: see `TODO.md`.

> **Session start:** read `.claude/session-commands.txt` and briefly show the user the skills.

## Layout

| Directory | Contents | Role |
|---|---|---|
| `cmd/dumb-server/` | Job queue + long-poll endpoints | **ACTIVE** |
| `cmd/dumb-worker/` | Background worker: manages the local Ollama, self-updates | **ACTIVE** |
| `internal/protocol/` | Wire types — the authority is `docs/protocol/openapi.yaml` | **ACTIVE** |
| `internal/queue/` | In-memory job queue with leases | **ACTIVE** |
| `internal/server/` | HTTP handlers for the protocol, rate limits (`ratelimit.go`), request-id logging | **ACTIVE** |
| `internal/auth/` | `Authenticator` interface + `StaticTokens` reference (hash-only, revocable) | **ACTIVE** |
| `internal/ollama/` | Loopback-only Ollama API client + detect / install / start (injectable seams) | **ACTIVE** |
| `internal/worker/` | Agent loop: hello, long-poll, run job, post result | **ACTIVE** |
| `internal/doctor/` | Readiness checks behind `dumb-worker doctor` | **ACTIVE** |
| `internal/service/` | launchd / systemd (hardened system unit as `User=dumb`) / Windows service definitions and install — see `docs/service.md` | **ACTIVE** |
| `internal/update/` | Self-updater: GitHub releases, minisign verification, atomic swap, probation / rollback. Worker only — the server must not import it | **ACTIVE** |
| `internal/update/updatetest/`, `internal/update/e2e/fakegh/` | Throwaway minisign keys/signatures and a fake GitHub Releases server for tests | test tooling |
| `.goreleaser.yaml`, `.github/workflows/` | Release build (`release.yaml`, on `v*` tags) and CI (`ci.yaml`) | **ACTIVE** |
| `docs/` | Protocol and design docs | — |
| `qa-automation/` | Build gate (unit tests only) | **ACTIVE** — follow `qa-automation/README.md` |

**This repository is public.** Everything written here must be safe with the whole world
reading it. Secrets, addresses, node inventory and the production auth implementation belong
in the private `ops` repository, never here.

## Sources of truth

1. **`docs/protocol/openapi.yaml`** — the protocol authority. A change to an endpoint changes
   the contract first, then the code, in the same commit.
2. `SECURITY.md` — the security design rules.
3. The workspace's `docs/adr/` — architecture decisions (outside this repository).

## Constants

- **DUMB = Distributed User-owned Model Backend.** Use the expansion in README and docs.
- **Security does not rely on secrecy.** Nothing in this repository depends on being hidden;
  it relies on secrets that are never in it.
- **Workers accept no inbound connections.** Outbound HTTPS (long-poll) to the server only.
  Ollama listens on `127.0.0.1` only.
- **Go, standard library first.** A third-party dependency in the server needs a written reason.
  The only dependency is `golang.org/x/crypto/blake2b`, used by the worker's updater for
  minisign; `TestServerHasNoThirdPartyDeps` keeps it out of the server.
- **Worker updates are signed, atomic and reversible.** A binary whose signature does not
  verify is never applied.
- Everything in this repository — code, comments, docs, commit messages — is in English.
- Secrets and test credentials are never committed; they arrive through `QA_`-prefixed
  environment variables.

## Commands

```bash
go run ./cmd/dumb-server    # run locally
go test ./...               # unit tests
QA_OLLAMA=1 go test -run Integration -v ./internal/ollama   # against a local Ollama
go build ./...              # build
./qa-automation/scripts/update-e2e.sh   # local signed self-update end to end (no GitHub)
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish  # dry-run release into dist/
```

## Releases

Details: `docs/release.md`. A release is cut **only on the user's explicit command** by pushing
a `vX.Y.Z` (or `vX.Y.Z-beta.N`) tag; `.github/workflows/release.yaml` runs the gate, goreleaser
(draft), signs `checksums.txt` with minisign, uploads `checksums.txt.minisig`, then publishes.
Repository configuration, by name only: secrets `MINISIGN_SECRET_KEY`, `MINISIGN_PASSWORD`;
variable `MINISIGN_PUBLIC_KEY` (embedded into workers via `-X .../internal/update.PublicKey`).
The key pair is owned by the private `ops` repository. Worker exit code 4 = "restart me"
(after an update or a rollback).

## QA

No UI tests (qa=none, a deliberate decision). The gate runs the unit tests only.

## Skills

| Skill | What it does |
|---|---|
| `/core-build` | Builds the artefact if the gate is green, then STOPS (no upload, no deploy) |

### Maintenance rule — when adding things, update these too

- **New skill:** write `.agents/skills/core-<name>/SKILL.md` (prefix required) → add a line to
  `.claude/session-commands.txt` → add a row to the table above.
- **Build step changes:** keep the `core-build` skill and `build_cmd` in the workspace config
  in sync.

## Global rules

- **Publishing only on explicit command.** A build produces an artefact and stops; push,
  deploy and release happen only when the user says so.
- **Verifiable goals.** A bug gets a test that reproduces it first. If a second fix fails,
  there is no third patch: find the root cause.
- **Record keeping:** a session opened in this directory records its work in this
  repository's git history and `TODO.md` — every item verifiable, and what was not done is
  written down too.
