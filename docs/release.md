# Releases and worker self-update

## Cutting a release

1. Review the public source/history boundary and run
   `GITLEAKS_BIN=/path/to/gitleaks ./qa-automation/scripts/public-source-audit.sh`
   with verified Gitleaks 8.30.0 after the gate. Make sure the gate is green: `./qa-automation/scripts/build-gate.sh`.
2. Tag and push: `git tag v0.2.0 && git push origin v0.2.0` (a pre-release is
   `v0.2.0-beta.1`). Only on an explicit decision; nothing pushes automatically.
3. `.github/workflows/release.yaml` then runs the gate again, builds with
   GoReleaser 2.18.3 (`.goreleaser.yaml`) into a **draft** release, builds native
   macOS worker applications for Apple silicon and Intel, adds their SHA-256
   entries to the same manifest, signs
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

Local dry run (no publish): `go run github.com/goreleaser/goreleaser/v2@v2.18.3 release --snapshot --clean --skip=publish`
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

**Server-recommended version.** An operator can set
`DUMB_RECOMMENDED_WORKER_VERSION` on `dumb-server` (embedders: `Config.RecommendedWorkerVersion`;
never below the floor). The server then returns `recommended_worker_version` in
the hello and `GET /v1/workers/me` answers. A worker older than it runs the same
check as the periodic one at once, instead of waiting up to 7 h: the release
still comes from the worker's own release source, is minisign-verified, swapped
atomically and put on probation; the server never supplies a binary or a URL.
The check drains like a periodic update, so jobs in flight finish first. It is
debounced: the same recommended version triggers at most one check per hour
(later hellos are ignored), a different newer one is checked at once, and a
version this worker rolled back (`<exe>.skip.json`) is never retried because of
a recommendation. A recommendation is advice only: an older worker is not
refused (that remains the `min_worker_version` `426`), and a channel that has no
release at that version simply finds nothing to install.

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

## macOS worker application

The release includes `dumb-worker-app_<version>_darwin_arm64.zip` for Apple
silicon and `dumb-worker-app_<version>_darwin_amd64.zip` for Intel. Each archive
contains `DUMB Worker.app`, its Go worker, native AppKit helper and icon resources.
The application archives are covered by the same minisign-verified checksum
manifest as the CLI archives. The standalone CLI signed updater continues to use the CLI archive names.
Binary-only self-updates are disabled inside the desktop application to preserve
the sealed bundle signature. Quit the worker and replace the complete app to
update it. An upgrade-required server response stops claims and shows an upgrade
error until the newer application is installed.

Build locally on macOS:

```sh
DUMB_BUILD_VERSION=0.3.3 DUMB_BUILD_ARCH=arm64 \
DUMB_MINISIGN_PUBLIC_KEY="$DUMB_RELEASE_PUBLIC_KEY" \
DUMB_BUILD_ZIP="$PWD/dist/dumb-worker-app_0.3.3_darwin_arm64.zip" \
./scripts/build-macos-app.sh
```

`DUMB_BUILD_ARCH` accepts `arm64` or `amd64` and defaults to the host Go
architecture. `DUMB_BUILD_ZIP` is optional and refuses an existing archive.
Both the helper and Go executable are ad-hoc code signed, then the bundle
signature and property lists are verified before activation. Ad-hoc signing
provides local code integrity; these builds have no Apple Developer ID signature
or notarization. Gatekeeper acceptance for quarantined downloads is not covered
by local builds. See [macos-worker.md](macos-worker.md).

macOS setup, native lifecycle and packaging acceptance are recorded in
[macos-worker-2026-10-16.md](qa/macos-worker-2026-10-16.md).

Local prerelease packaging evidence and remaining platform limitations are in
[release-preflight-2026-10-15.md](qa/release-preflight-2026-10-15.md). The snapshot
archive verifier is `qa-automation/scripts/check-release-archives.py DIST PUBLIC_KEY`. Add
`--version VERSION --commit COMMIT --require-apps` to verify a published release
with both native application archives.
It checks packaging and embedded public metadata; real signature/update/rollback
acceptance must use the published release assets.
