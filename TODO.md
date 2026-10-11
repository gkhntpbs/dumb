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
- [x] Agent loop: hello, long-poll, generate with the lease deadline, post result; 426 stops with exit code 3, 401 stops. → verify: `internal/worker` tests against a real `internal/server`; live smoke with a real Ollama.
- [x] `GET /v1/workers/me`: side-effect-free credential/version check for `doctor`. → verify: `TestWhoAmI`, `TestTypesMatchSpec`, redocly lint.
- [ ] Windows service: the binary does not implement the Service Control Manager protocol, so `sc.exe start` will time out. Needs SCM handling (stdlib `syscall` or an ADR for `golang.org/x/sys/windows/svc`). → verify: service starts on Windows.
- [ ] launchd `KeepAlive=true` restarts the worker every ~10s after exit code 3 (upgrade required). Hook the self-updater in, or use `KeepAlive: {SuccessfulExit: false}` with a backoff. → verify: an outdated worker does not spin.
- [ ] Linux system unit runs as root; add a dedicated `User=`. → verify: generated unit test.
- [ ] Re-hello when Ollama's model list changes (today models are fixed by config). → verify: test.
- [ ] `setup` end-to-end on a machine without Ollama (real install). → verify: manual run on a clean VM.

## Updates / release
- [ ] Release pipeline: GitHub Actions + goreleaser, macOS / Windows / Linux binaries. → verify: a tag produces all artefacts.
- [ ] Signed releases; the worker embeds the public key and refuses unsigned or mis-signed binaries. → verify: test with a tampered binary.
- [ ] Atomic replace + automatic rollback if the new version cannot reach the server. → verify: test with a deliberately broken release.
- [ ] Channels (`stable` / `beta`). → verify: a beta release reaches only beta workers.

## Security
- [x] `Auth` interface (per-worker credential, revocable, only a hash stored) + a simple static-token reference implementation. → verify: tests for missing / wrong / revoked credentials (401).
- [~] Request body limits and rate limits on worker endpoints. → verify: test. *Body limit (1 MiB, `TestBodyLimit`) done; rate limits not yet.*
- [x] A result is accepted only from the worker that holds the lease. → verify: test.

## Repository
- [ ] CI runs `qa-automation/scripts/build-gate.sh`. → verify: a failing test turns CI red.
