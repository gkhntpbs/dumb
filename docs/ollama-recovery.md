# Ollama readiness and recovery

`dumb-worker run` and `setup` share `internal/ollama/recovery.go`.
On macOS, an implicit default endpoint may start a discovered, stopped Ollama
CLI or install it when absent. A reachable API is reused; missing configured
models are pulled without deleting other models or changing their settings.
The worker verifies its server credential before any readiness repair, stops
advertising unavailable models when failure is detected, and says hello again
when readiness returns. API checks during `run` occur every five seconds;
generation failures also request an immediate readiness check.

## Endpoint and process ownership

A non-empty `DUMB_OLLAMA` or an explicit `-ollama` flag is external, even when
its value is the default `http://127.0.0.1:11434`. That provenance is preserved
in generated service environments. An empty environment value selects the
implicit default. Non-default endpoints and non-macOS systems are not
installed or started automatically by this readiness manager.

A successful model-list observation or version probe of an API without an
in-memory owned child handle marks it external before a later outage can
trigger installation. A custom, previously running, occupied, malformed or
hanging unmanaged endpoint is never replaced, killed or repaired by starting
a different process over it. Its owner must restore it. Models may be added
through an already reachable configured API, but existing model data is not
removed.

Only a CLI child started by this worker has a process handle eligible for
restart or termination. The worker starts the macOS bundle's
`Contents/Resources/ollama serve` directly, with `OLLAMA_HOST` bound to
127.0.0.1:11434; it never launches the GUI with `open -a`. A failed owned
version/tags check or owned generation connection-refused/server-5xx failure can stop
that child and start it again within the bounded repair cycle. Request/model
4xx failures do not request a process restart. Unmanaged model/backend faults
are reported without destructive repair; this is not a promise to repair
arbitrary model or hardware failures.

`setup` prepares the installation/models and stops only a CLI child it started
when it exits. An API already running remains untouched. Its next command is
`dumb-worker run`, which owns a stopped CLI throughout its runtime and stops
its own child on normal shutdown. No PID ownership is persisted and no unknown
PID is signaled. If the worker is killed without cleanup, a still-running
orphan API is conservatively external to the next worker. A managed bundle
pathname alone is not proof of socket ownership; automatic orphan adoption
and recovery are not implemented.

## Bounds and installation

| Operation | Default bound |
|---|---|
| Repair attempts per cycle | 3 |
| Version/model probe | 3 s |
| Start/readiness wait | 30 s |
| Installer download/extraction | 5 min |
| Configured model pull | 15 min per model |
| Retry delay within cycle | 1 s, then 2 s |
| Failed-cycle cooldown | 1 min, doubles to 10 min |
| Owned process stop | 5 s graceful budget, then own-process kill |

Permission, disk-full, existing-target and protected-endpoint failures stop a
cycle immediately. All waits and HTTP operations accept cancellation. Models
are requested serially; no startup/install retry runs in a busy loop.

On macOS, the official HTTPS archive is downloaded to a newly created staging
directory under `~/Applications`. Compressed download size is limited to
2 GiB and total declared extracted size to 8 GiB. Downloading streams to a
0600 file; it does not load a multi-gigabyte installer into memory. HTTPS
redirects cannot downgrade to HTTP. Existing download files are not overwritten.

Archive validation rejects absolute/traversing paths, duplicates, escaping
symlinks and file writes through archive symlink ancestors. Parent components
in entry names and symlink targets are rejected. Contained relative symlinks
remain supported. After extraction, the CLI must be a regular executable
before publication. `~/Applications/Ollama.app` is published with an atomic
relative symlink to the retained `.dumb-ollama-install-*/Ollama.app` bundle.
An existing file, directory or symlink cannot be overwritten, including a
concurrent publication. The downloaded archive is removed after publication;
failed stages are removed. The verified retained bundle is not filesystem
immutable and is not automatically deleted or replaced.

These production safety limits are separate from the tiny test fixtures.
No official installer, native Ollama process, model download or real clean-Mac
installation was executed for this change. Native macOS startup, code-signing,
OS notifications, child-process cleanup and focus behavior still need a real
opt-in check on a suitable clean machine. Windows tray/service work remains
deferred; the existing macOS BTM notification gap is still open.

Official layout references: [Ollama macOS documentation](https://github.com/ollama/ollama/blob/main/docs/macos.mdx)
and [macOS build script](https://github.com/ollama/ollama/blob/main/scripts/build_darwin.sh).

## Lightweight isolated fault lab

Run from core:

```sh
./qa-automation/scripts/recovery-lab.sh
```

The lab uses `internal/recoverylab`, tiny inert zip/CLI payloads, in-memory
model lists and `httptest` APIs. Each fixture uses a temporary root opened
through `os.Root`; writes, extraction, symlink publication and cleanup stay
inside it. A simulated runner accepts only the exact fixture extraction and
CLI-start shapes and executes no native commands. The downloader writes a
tiny local archive and never contacts an installer server. HTTP transports
deny unregistered destinations; the logical default API URL is translated
to a registered random loopback fixture port before any socket is opened.
The real 11434 endpoint is never contacted. Escape tests reject native shell
or GUI launches, out-of-root writes, external network destinations and a real
11434 socket. Temporary roots are checked for removal after their scope.

This is a fail-closed dependency-injection harness for trusted test code, not
an OS sandbox, VM, container or environment for running untrusted code.
No real app/service is installed, removed, started or reconfigured. No real
model assets are loaded or downloaded. There is no dependency on replacing
`HOME`, and no global Go cache is cleared.

The script compiles three test binaries serially and runs fresh tests with
`GOMAXPROCS=2`, `GOMEMLIMIT=256MiB`, `GOFLAGS=-p=1`, `-parallel=1`,
`-count=1`, and a 30 s per-binary test timeout. `QA_OLLAMA` and `QA_LAUNCHD`
are disabled. The memory limit is a soft Go runtime target, not an OS hard cap
or a bound on the compiler/Node processes in the full gate. On macOS,
`/usr/bin/time -l` measures each test process separately; `du -sk` reports
compiled scratch size. Only the newly created scratch directory is removed.

The fault matrix covers present/stopped/absent installations, missing models,
proxy preservation, interrupted/corrupt downloads, extraction/permission/
disk-full/publication failures, launch failure/crash, hanging/malformed/
unavailable APIs, interrupted/hanging/missing-model pulls, cooldown,
cancellation and cleanup. Owned API/tag/generation failures recover through
real DUMB queue jobs. Unauthenticated workers perform no repair. CLI tests
cover explicit-default provenance, service preservation and setup ownership.
Dated measurements and red-before-fix evidence are in `measurements.md`.
