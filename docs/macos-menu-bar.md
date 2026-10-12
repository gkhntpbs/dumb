# macOS menu bar worker

The local macOS application uses a Go worker and a small native AppKit helper.
It creates only an icon in the menu bar. Both bundles declare `LSUIElement` and
the helper uses the accessory activation policy. There are no application
windows, Dock controls, dialogs, notifications or foreground activation calls.
Windows tray work remains deferred.

Build locally on macOS with the installed Go and Swift compiler:

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB ./scripts/build-macos-app.sh
```

The output is `dist/DUMB Worker.app`. Repeated builds replace only an output
carrying the exact local-build ownership marker; unmarked outputs and symlinks
are refused. Compilation happens in a newly created sibling staging directory.
`DUMB_BUILD_VERSION` defaults to `0.0.0`; a release version may be supplied
explicitly. This creates a local build, not a signed or notarized distribution.
The existing GoReleaser archives still contain CLI binaries. Native helper
signing, notarization and release packaging remain pending.

Configuration uses the existing worker flags/environment and mode-0600 token
file. Run the application's `Contents/MacOS/dumb-worker` with these settings, or
use its `service install` command to generate the existing launchd configuration.
No service is installed by the build. An unbundled macOS CLI intentionally uses
`run -headless` or `DUMB_HEADLESS=1`; development and command-line QA scripts opt
in explicitly. Linux and Windows remain headless. No credential, server address,
prompt, model name or raw error is sent to the helper.

The menu shows actual readiness, recovery, active job count and lifecycle.
Backend recovery failures show an error category; fatal credentials and upgrade
errors have their own categories. An update check is visible as updating, without
overriding a pause, drain or error. Active job count comes from the worker;
successful server credential checks alone never mean ready.

Pause immediately cancels claim polling and recovery, then allows active jobs
to finish under their existing lease deadlines. After drain, Resume creates one
new worker using the same readiness manager. Resume is disabled during drain.
Quit may supersede a pause while jobs drain and exits successfully after jobs
finish. A launchd definition uses `KeepAlive: {SuccessfulExit: false}` so Quit
remains stopped until the next login, service bootstrap or explicit start;
update exit 4, crashes and other unsuccessful exits still restart. Fatal errors
with a live menu retain controls and do not repeatedly restart a worker.

The helper is executed directly as a child, with JSON-lines over stdin/stdout;
it opens no listener. Protocol version 1 requires a ready handshake within five
seconds. Messages contain only fixed state/category enums and a nonnegative
count. Input is bounded to 4096 bytes, controls are bounded and unknown controls
are rejected. Missing, incompatible or dead helpers stop new claims and drain
active jobs. The worker then remains stopped until process shutdown, without
retrying the helper in a loop. A probationary new binary rolls back when helper
startup fails. The existing signed updater replaces only the Go executable;
it never replaces the native helper. A helper protocol change needs an explicit
compatible application distribution.

Verify without host Ollama or native installers:

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB go test -p 1 -count=1 ./internal/menubar ./cmd/dumb-worker ./internal/worker ./internal/service
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 ./qa-automation/scripts/build-gate.sh
```

The native protocol test compiles the helper and runs `--self-test` before any
AppKit application or workspace access. It validates 45 state/category pairs
and malformed messages. Queue lifecycle tests use the sealed recovery lab,
random loopback HTTP fixtures and runtime-generated credentials.

A real transient menu preview is opt-in:

```sh
QA_MENU_BAR_HELPER="$PWD/dist/DUMB Worker.app/Contents/Resources/DUMBMenu.app/Contents/MacOS/dumb-menu" \
QA_MENU_DIAGNOSTICS="$PWD/dist/menu-diagnostics.jsonl" \
QA_MENU_STAGE_FILE="$PWD/dist/menu-stage" \
GOMAXPROCS=2 GOMEMLIMIT=256MiB go test -p 1 -count=1 -v ./cmd/dumb-worker -run '^TestMenuNativePreview$'
```

It serves a real queue job through the sealed fake Ollama, then waits up to
180 seconds for menu controls. The optional stage file accepts `error`,
`updating` and `ready` to exercise display states; update/error displays in this
preview are injected, not downloaded updates or host failures. Diagnostics
record only state/count, framework window classes, content-window count,
activation policy and whether foreground focus stayed unchanged. The explicit
QA preview also records the status-button framework window number for tool
attachment diagnostics. A status bar
framework window is expected and is counted separately from content windows.
The file does not contain application names, process IDs or credentials.

No real installer, launchd installation, quarantined release or operating-system
background-item notification is exercised by this preview. macOS-owned BTM
notifications remain unresolved; this implementation does not suppress system
protections. Runtime diagnostic evidence and any unavailable visual verification
are recorded separately in `measurements.md`.

Set `QA_MENU_AUTOMATED=1` for the finite native diagnostic run. Pause/Resume/Quit
are then sent through a fixture channel, not clicked in the menu. The test
checks real queue completion, native short-line display updates, zero content
windows, accessory policy and unchanged foreground focus. Actual menu clicks
and screenshot acceptance remain separate from this automated evidence.

## Uncoil development preview

Run `./scripts/menu-preview.sh` from this repository, or start the workspace's
`DUMB worker — Menu bar preview` Uncoil run configuration. This compiles the existing
sealed queue/recovery fixture and starts the actual native helper. It accepts
menu Pause, Resume and Quit, and terminal input `pause`, `resume` and `quit`.
Terminal input `error`, `updating` and `ready` injects display states only.
Uncoil Stop sends termination to the managed process group; the preview handles
SIGINT/SIGTERM and closes its worker, local fixture and native helper. Restart
starts a fresh fixture.

This explicit interactive preview is bounded to eight hours, with a 20-second
long poll and a two-second model check instead of the fast automated-test
intervals. `QA_MENU_PREVIEW_SECONDS` accepts 1 through 28800 for direct test
invocations; the existing direct preview defaults to 180 seconds. Duration
expiry stops the worker and reports a test timeout. The launcher clears
`QA_MENU_AUTOMATED` and native diagnostics, builds serially with a 256 MiB Go
memory target and refuses unowned or symlinked application outputs. Build
outputs remain under ignored `dist/`; Go compiler cache usage is separate.

No host Ollama endpoint, downloaded model, installer, service or production
server is used. The native helper is real; jobs and backend failures are sealed
fixtures. Terminal control tests do not establish actual menu-click or screenshot
acceptance.
