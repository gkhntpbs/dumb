# TODO — DUMB core

Status: `[ ]` to do · `[~]` partial / investigating · `[x]` done

---

## Protocol
- [ ] Define the worker protocol in `docs/protocol/openapi.yaml`: `GET /v1/jobs/next?wait=` (long-poll), `POST /v1/jobs/{id}/result`, worker hello (version, models, capacity). → verify: spec validates; Go types in `internal/protocol` match it.
- [ ] Define the caller API: submit a job, wait for its result with a deadline. → verify: spec validates.
- [ ] Minimum-worker-version answer (like a client floor): an outdated worker gets a clear error, not a broken job. → verify: test.

## Server
- [ ] In-memory queue with leases; a lease that expires is not re-dispatched (the caller's deadline has passed). → verify: unit tests for claim / complete / expiry.
- [ ] Long-poll endpoint that returns the moment a job is queued. → verify: test measures added latency < 50ms.
- [ ] "Is anyone online" answer, so callers skip waiting when no worker is connected. → verify: test.

## Worker
- [ ] Detect an existing Ollama; install it the official way when absent. → verify: `doctor` reports both cases correctly.
- [ ] Pull the required model through the Ollama API. → verify: integration test against a local Ollama.
- [ ] Run as a service: launchd / Windows service / systemd. → verify: survives reboot on a Mac.
- [ ] `doctor` command: Ollama reachable, model present, server reachable, credential valid, sample latency. → verify: each check fails visibly when broken.

## Updates / release
- [ ] Release pipeline: GitHub Actions + goreleaser, macOS / Windows / Linux binaries. → verify: a tag produces all artefacts.
- [ ] Signed releases; the worker embeds the public key and refuses unsigned or mis-signed binaries. → verify: test with a tampered binary.
- [ ] Atomic replace + automatic rollback if the new version cannot reach the server. → verify: test with a deliberately broken release.
- [ ] Channels (`stable` / `beta`). → verify: a beta release reaches only beta workers.

## Security
- [ ] `Auth` interface (per-worker credential, revocable, only a hash stored) + a simple static-token reference implementation. → verify: tests for missing / wrong / revoked credentials (401).
- [ ] Request body limits and rate limits on worker endpoints. → verify: test.
- [ ] A result is accepted only from the worker that holds the lease. → verify: test.

## Repository
- [ ] CI runs `qa-automation/scripts/build-gate.sh`. → verify: a failing test turns CI red.
