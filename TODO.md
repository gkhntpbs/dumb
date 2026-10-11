# TODO — DUMB core

Status: `[ ]` to do · `[~]` partial / investigating · `[x]` done

---

## Protocol
- [x] Define the worker protocol in `docs/protocol/openapi.yaml`: `GET /v1/jobs/next?wait=` (long-poll), `POST /v1/jobs/{id}/result`, worker hello (version, models, capacity). → verify: spec validates; Go types in `internal/protocol` match it.
- [x] Define the caller API: submit a job, wait for its result with a deadline. → verify: spec validates.
- [x] Minimum-worker-version answer (like a client floor): an outdated worker gets a clear error, not a broken job. → verify: test.

## Server
- [x] Fix: a caller wait clamped to the deadline could return `queued` instead of `expired` (race with the expiry timer; `TestSubmitReturns202ThenExpires` was flaky). → verify: `TestWaitAtDeadlineReportsExpired`.
- [ ] Enforce the capacity announced in hello (today a worker limits itself by how many polls it runs). → verify: test.
- [ ] Request-id / structured logging for claim, result and expiry. → verify: log lines in a test.
- [x] In-memory queue with leases; a lease that expires is not re-dispatched (the caller's deadline has passed). → verify: unit tests for claim / complete / expiry.
- [x] Long-poll endpoint that returns the moment a job is queued. → verify: test measures added latency < 50ms.
- [x] "Is anyone online" answer, so callers skip waiting when no worker is connected. → verify: test.

## Worker
- [~] Detect an existing Ollama; install it the official way when absent. → verify: `doctor` reports both cases correctly. *Detect verified for real (`doctor` → "Ollama installed") and both cases unit-tested (`TestDetect`, `TestEachCheckFails`). `Install` (Linux script / macOS zip+ditto / Windows /VERYSILENT) is unit-tested with fake runners only — never executed for real.*
- [x] Pull the required model through the Ollama API. → verify: integration test against a local Ollama. *`QA_OLLAMA=1 go test -run Integration -v ./internal/ollama` (smollm2:135m).*
- [~] Run as a service: launchd / Windows service / systemd. → verify: survives reboot on a Mac. *`internal/service` generates + installs plist / systemd unit / `sc.exe create`, unit-tested with a fake runner; never installed on a real machine, reboot survival not verified — needs Gökhan on a Mac.*
- [x] `doctor` command: Ollama reachable, model present, server reachable, credential valid, sample latency. → verify: each check fails visibly when broken. *`TestEachCheckFails`; run live against a local dumb-server with good / wrong token.*
- [x] Agent loop: hello, long-poll, generate with the lease deadline, post result; 401 stops; 426 now triggers the self-updater instead of exit code 3 (see Updates / release). → verify: `internal/worker` tests against a real `internal/server`; live smoke with a real Ollama.
- [x] `GET /v1/workers/me`: side-effect-free credential/version check for `doctor`. → verify: `TestWhoAmI`, `TestTypesMatchSpec`, redocly lint.
- [ ] Windows service: the binary does not implement the Service Control Manager protocol, so `sc.exe start` will time out. Needs SCM handling (stdlib `syscall` or an ADR for `golang.org/x/sys/windows/svc`). → verify: service starts on Windows.
- [x] launchd `KeepAlive=true` restarts the worker every ~10s after exit code 3 (upgrade required). Hook the self-updater in, or use `KeepAlive: {SuccessfulExit: false}` with a backoff. → verify: an outdated worker does not spin. *On 426 the worker updates or stays up with a 15 min → 2 h backoff; exit code 3 is gone, exit 4 = restart (`TestUpgradeRequiredWithoutUpdateStaysUp`; `update-e2e.sh` run 2 shows "staying up" with no exit). Plist gains `ThrottleInterval` 10, Windows gains `sc.exe failure` restart actions (`TestPlistIsValidXML`, `TestInstallUninstall`).*
- [ ] Linux system unit runs as root; add a dedicated `User=`. → verify: generated unit test.
- [ ] Re-hello when Ollama's model list changes (today models are fixed by config). → verify: test.
- [ ] `setup` end-to-end on a machine without Ollama (real install). → verify: manual run on a clean VM.

## Updates / release
- [~] Release pipeline: GitHub Actions + goreleaser, macOS / Windows / Linux binaries. → verify: a tag produces all artefacts. *`.goreleaser.yaml` + `.github/workflows/release.yaml`; `goreleaser check` passes and `goreleaser release --snapshot --clean --skip=publish` produced all 10 archives (worker + server × darwin/arm64, darwin/amd64, linux/amd64, linux/arm64, windows/amd64) + `checksums.txt`. A real tag push through Actions (draft → sign → publish) has not run yet.*
- [x] Signed releases; the worker embeds the public key and refuses unsigned or mis-signed binaries. → verify: test with a tampered binary. *minisign `ED` signature over `checksums.txt` (`TestMinisignVerify`, `TestBadSignatureNeverApplied`, `TestTamperedArchiveNeverApplied`); real binaries: `qa-automation/scripts/update-e2e.sh` run 2 refuses a tampered archive and leaves 0.1.0 byte-identical.*
- [x] Atomic replace + automatic rollback if the new version cannot reach the server. → verify: test with a deliberately broken release. *`TestRollbackWhenServerUnreachable`, `TestCrashLoopRollback`, `TestSwapFailureRestoresBinary`, `TestProbationRollsBackWithoutHello`; `update-e2e.sh` run 1: 0.1.0 → 426 → 0.2.0 → exit 4 → hello → commit. A real rollback of a real binary was only exercised in unit tests (temp files), not end to end.*
- [x] Channels (`stable` / `beta`). → verify: a beta release reaches only beta workers. *`TestChannels`, `TestPick`.*
- [ ] ops: generate the production minisign key pair, keep the secret key in ops, and set `MINISIGN_SECRET_KEY` / `MINISIGN_PASSWORD` (secrets) and `MINISIGN_PUBLIC_KEY` (variable) on the GitHub repository. → verify: the first tagged release publishes `checksums.txt.minisig` and a released worker updates itself.
- [ ] First real release: push a tag and check the draft → sign → publish flow (minisign reading the password from stdin in Actions is untested). → verify: release page has all archives, `checksums.txt` and `.minisig`; `minisign -V` passes.
- [ ] Re-run `update-e2e.sh` on Linux and Windows (only run on macOS/arm64). Windows: rename of the running `.exe` and the `.bad` cleanup are untested. → verify: script passes.
- [ ] A long-poll aborted by `Drain` may lose a job the server leased in the same instant (it expires at its deadline). → verify: test, or have the server release the lease on disconnect.
- [ ] macOS: a self-updated binary is not notarized/codesigned by us (Go ad-hoc signs on arm64); check Gatekeeper never blocks a launchd agent after update. → verify: update on a Mac with a real release.

## Security
- [x] `Auth` interface (per-worker credential, revocable, only a hash stored) + a simple static-token reference implementation. → verify: tests for missing / wrong / revoked credentials (401).
- [~] Request body limits and rate limits on worker endpoints. → verify: test. *Body limit (1 MiB, `TestBodyLimit`) done; rate limits not yet.*
- [x] A result is accepted only from the worker that holds the lease. → verify: test.

## Repository
- [~] CI runs `qa-automation/scripts/build-gate.sh`. → verify: a failing test turns CI red. *`.github/workflows/ci.yaml` runs the gate, `go vet` (linux/windows/darwin) and gofmt on push/PR; never run — no remote is configured and Actions cannot run locally.*
