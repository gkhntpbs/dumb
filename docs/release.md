# Releases and worker self-update

## Cutting a release

1. Make sure the gate is green: `./qa-automation/scripts/build-gate.sh`.
2. Tag and push: `git tag v0.2.0 && git push origin v0.2.0` (a pre-release is
   `v0.2.0-beta.1`). Only on an explicit decision; nothing pushes automatically.
3. `.github/workflows/release.yaml` then runs the gate again, builds with
   goreleaser (`.goreleaser.yaml`) into a **draft** release, signs
   `checksums.txt` with minisign, checks that signature against the public key,
   uploads `checksums.txt.minisig` and only then publishes the release.
   goreleaser marks tags with a pre-release suffix as GitHub pre-releases.

Artefacts: `dumb-worker_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) and the
same for `dumb-server`, for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64
and windows/amd64 (`CGO_ENABLED=0`, `-trimpath`), plus `checksums.txt` (SHA-256)
and `checksums.txt.minisig`.

Repository configuration (names only):

| Name | Kind | Contents |
|---|---|---|
| `MINISIGN_SECRET_KEY` | secret | the encrypted minisign secret key file |
| `MINISIGN_PASSWORD` | secret | its password |
| `MINISIGN_PUBLIC_KEY` | variable | the public key line (`RW...`), embedded in every worker |

The key pair is generated and kept by the private `ops` repository. Without
`MINISIGN_PUBLIC_KEY` the workflow stops; a worker built without a key logs
`no release key embedded; self-update disabled` and never updates.

Local dry run (no publish): `go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish`
(output in the git-ignored `dist/`). Local end-to-end update test without
GitHub: `./qa-automation/scripts/update-e2e.sh`.

## Signing

Releases are signed with [minisign](https://jedisct1.github.io/minisign/), using
standard prehashed signatures (`ED`: Ed25519 over BLAKE2b-512 of the file). The
worker verifies them itself (`internal/update/minisign.go`, stdlib
`crypto/ed25519` plus `golang.org/x/crypto/blake2b`, the only third-party
dependency, which the server does not link — `TestServerHasNoThirdPartyDeps`).
It checks the key id, the signature over the file, and the global signature over
the trusted comment; legacy non-prehashed `Ed` signatures are refused.

What is signed is `checksums.txt`. A worker:

1. downloads `checksums.txt` and `checksums.txt.minisig` and verifies the signature
   **first**;
2. downloads the archive for its OS/arch and compares its SHA-256 with the entry
   in the verified `checksums.txt`; the archive name carries the version, so an
   older signed release served under a newer tag does not match;
3. only then opens the archive and extracts `dumb-worker`.

Any failure leaves the running binary untouched.

## Channels and triggers

`-channel` / `DUMB_CHANNEL`: `stable` (default) sees releases only; `beta` sees
pre-releases too. The newest version wins (a pre-release sorts below its release:
`0.2.0-beta.1 < 0.2.0`). Versions that were rolled back are never picked again
(`<exe>.skip.json`).

The worker checks at startup, every 6 h (plus up to 1 h jitter), and at once
when the server answers `426`; then the release must be at least the server's
`min_worker_version`. If no such release exists, or updates are disabled, the
worker does **not** exit: it logs and checks again after 15 min, doubling up to
2 h, so a service manager never restarts it in a loop. A periodic update drains
first: no new claims, running jobs finish (bounded by their lease deadlines).

## Apply, probation and rollback

```
                 Update()                        exit 4                 first hello 200
 running 0.1.0 ───────────► 0.2.0 on disk ─────────────► 0.2.0 on probation ──────────────► committed
                 .new→fsync    .prev = 0.1.0              attempts += 1                      state, .prev deleted
                 exe→.prev     state file written              │
                 .new→exe                                       │ no hello within 2 min, or
                                                                │ more than 3 starts (crash loop)
                                                                ▼
                                                rolled back: .prev→exe, 0.2.0 added to
                                                skip list, state deleted, exit 4 → 0.1.0
```

Files next to the binary (`<exe>` = resolved path of `dumb-worker`):

| File | Meaning |
|---|---|
| `<exe>.new` | the verified new binary while it is being written (fsync, chmod 0755) |
| `<exe>.prev` | the previous binary, kept until the new one commits |
| `<exe>.update.json` | probation state: `from`, `to`, `attempts`, `started_at` |
| `<exe>.skip.json` | versions that were rolled back |
| `<exe>.bad` | a rolled-back binary that could not be deleted yet (Windows) |

Every step is a rename in one directory, which is atomic and allowed for a
running executable on every OS. While a state file exists no new update starts.

Exit code **4** means "restart me now". launchd (`KeepAlive`, `ThrottleInterval`
10 s: immediate when the process ran at least 10 s), systemd (`Restart=always`,
5 s) and the Windows service (`sc.exe failure` restart actions) all restart it.
