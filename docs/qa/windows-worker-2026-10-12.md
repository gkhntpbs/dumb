# Windows worker verification — 2026-10-12

This run used Windows 11 Pro / amd64 and a checksum-verified portable Go 1.26.5
in an isolated development directory. The process used private build/module
caches, `GOMAXPROCS=2` and `GOFLAGS=-p=1`. No global tool installation, unrelated existing
service/Ollama/VM change, reboot, publication or push was performed. Real later
authorized acceptance installed only the owned portable Ollama and created then
removed uniquely named fixture services. Host
coordinates and local user details are deliberately excluded from this public
record.

## Native Windows tests

Run from the isolated repository in PowerShell:

```powershell
. ./scripts/windows-dev-env.ps1 -Offline
go test -count=1 ./internal/menubar ./internal/service ./internal/worker ./internal/update ./internal/ollama ./cmd/dumb-worker
./scripts/build-windows-worker.ps1
go build -trimpath -ldflags '-H windowsgui' -o dist/windows/tray-preview.exe ./cmd/tray-preview
```

All six native packages passed with `-count=1`: menubar 0.393s, service 0.216s,
worker 1.758s, update 1.058s, Ollama 0.870s, worker command 0.625s. The portable
worker/tray build and GUI-subsystem preview compilation exited 0. These results
validate native unit/integration fixtures; compilation does not establish tray
visibility or foreground behavior.

The selected verbose native test output is preserved in
[windows-native-tests-2026-10-12.txt](windows-native-tests-2026-10-12.txt),
normalized from the downloaded Windows log to UTF-8/LF. All seven ACL variants,
owned/console-free process cases, real running-executable swap/rollback, Windows
ZIP update, sibling helper validation and SCM stop/restart cases passed.

The PE headers of `dumb-worker.exe`, `dumb-tray.exe` and `tray-preview.exe`
reported subsystem 2 (Windows GUI). Native PowerShell parser checks passed for
`build-windows-worker.ps1` and `windows-tray-evidence.ps1`. These checks establish
build metadata and script syntax; they do not establish actual desktop visuals.

The native coverage includes:

- Token DACLs: owner/current-user, SYSTEM and Administrators grants; rejection
  of untrusted read, write, ownership and ACL grants. Elevated creation may set
  Administrators as owner while the current user has an explicit grant; both
  ownership and access grants are checked independently against trusted SIDs.
- Console-free owned subprocess creation and scoped shutdown of the owned PID.
- SCM start/interrogation/stop/shutdown through simulated control channels,
  graceful cancellation, service-specific exit 4 and generated recovery commands.
- Updating and rolling back an actual running Windows executable in a temporary
  directory (`TestWindowsRunningExecutableSwapRollback`), exercising native
  executable sharing/rename behavior.
- Existing worker, signed archive verification, rollback and loopback Ollama
  client tests included in the selected packages.

The sealed macOS app-install recovery lab explicitly skips on Windows because
its app archive and executable-mode filesystem fixture require POSIX semantics.
POSIX token-mode checks also skip in favor of native ACL tests; executable-mode
assertions apply only on POSIX. Windows tests normalize fixture path separators.
No GUI `OllamaSetup.exe` was run. The later authorized real acceptance uses the
full official standalone CLI ZIP; no installer flags, PATH changes or autostart
registration are involved.

## macOS regression gate

The complete existing gate, including unrelated pre-existing admin work, passed:

```bash
./qa-automation/scripts/build-gate.sh
```

Focused worker/runtime regressions also passed on macOS; Windows cross-target
`go vet` passed for the worker, tray, service, worker runtime, Ollama and updater.
The macOS gate's GUI policy remains `qa=none`; native helper protocol checks are
part of Go tests and do not replace Windows visual acceptance.

## Manual desktop fixture acceptance

The user ran the supplied command manually in the RDP desktop. The agent did
not launch a GUI or register an interactive scheduled task. The returned
[metrics](windows-tray-manual-2026-10-12/metrics.json) report unchanged startup
foreground, zero visible owned top-level windows and one native helper.
The [sanitized log](windows-tray-manual-2026-10-12/preview.log) includes all six
fixture states and actual Pause → paused, Resume → ready and Quit controls.
The [cropped native menu](windows-tray-manual-2026-10-12/tray-menu.png) was viewed
and shows the native status row and Pause/Quit controls. The notification-area
crop is not retained because it contains unrelated taskbar icons; the generic
helper icon may be placed in Windows' overflow area.

```powershell
./scripts/windows-tray-evidence.ps1 `
  -Preview ./dist/windows/tray-preview.exe `
  -Helper ./dist/windows/dumb-tray.exe `
  -Output ../evidence/tray-manual `
  -DurationSeconds 120 -OpenMenu
```

Use absolute paths when launching outside the repository. This finite inert
fixture never starts a worker, opens sockets, installs Ollama or changes
services. It records sanitized status/control logs and startup foreground/owned
visible-window counts, then crops only the notification area and its own menu.
`-OpenMenu` is an explicit QA interaction after startup focus measurement.
Manual inert menu visibility and Pause/Resume/Quit acceptance are complete.
Explorer restart recovery and actual real Ollama/install/update desktop lifecycle
visuals remain separate checks; synthetic fixture states do not prove them.

Actual SCM registration/start/stop/restart/removal and signed worker update
end-to-end are verified below. Reboot persistence remains unverified because
reboot was forbidden. Published production releases, actual GUI installer
behavior and real-worker RDP lifecycle visuals remain outside these checks.


## Authorized real lifecycle acceptance

Later explicit authorization permitted real official Ollama installation,
model-preserving owned uninstall/reinstall, automatic recovery, signed worker
updates and a uniquely named native test service. All tests ran headlessly;
the agent did not launch the tray automatically.

The original official `ollama-windows-amd64.zip` v0.40.3 contains 87 entries and
1,941,752,691 declared extracted bytes. Its SHA256 was verified on both the
secure download host and the Windows host:
`e5ecff02dfc7cae8ddf6d6b7a7ac5f727ffeedf4491f44ae75ff5b0820dbbbd5`.
The full official package was staged and installed by the production installer,
not replaced by inert binaries. Initial installation used the strict local
archive/SHA override after secure relay. A separate direct actual worker
`setup` subsequently used verified HTTPS with those overrides removed.

| Native case | Result | Versioned evidence |
|---|---|---|
| Official HTTPS archive and registry probe | PASS 1.64s; HTTP 200 and 404 respectively, with default certificate verification | [probe log](windows-official-https-2026-10-12.txt) |
| Real official installation/start/pull/generation/recovery/reinstall | PASS 43.31s; Ollama 0.40.3, `smollm2:135m` | [allowlisted lifecycle log](windows-real-ollama-2026-10-12.txt) |
| Actual GUI-subsystem worker `run` with real Ollama | PASS 4.66s; real queue result, forced-exit owned-tree cleanup | [CLI run log](windows-real-cli-run-2026-10-12.txt) |
| Actual unique SCM registration/start/job/stop/restart/delete | PASS 2.57s; two jobs, canceled long poll, goodbye and own service removed | [SCM log](windows-real-scm-2026-10-12.txt) |
| Signed GUI worker update/restart/commit and rollback | PASS 7.03s; zero visible owned windows | [signed updater log](windows-signed-update-2026-10-12.txt) |
| Combined signed GUI worker update under native SCM recovery | PASS 11.56s; automatic update/crash restarts, signature refusal and both services removed | [SCM signed-update log](windows-scm-signed-update-2026-10-12.txt) |

The model pull downloaded approximately 270 MB. Real generation used one CPU
affinity, one parallel job/model, context 512 and eight output tokens. Local
model tests disable cloud use. Jobs succeeded before and after the owned
Ollama process was killed. An external observer reused the already running API,
and its cancellation did not stop or replace the owner's process. Binary-only
owned uninstall/reinstall retained downloaded models and served another job.
The actual worker `setup` entry point also completed successfully.

Direct automatic HTTPS acceptance verified the ownership marker and an empty
default endpoint, removed only the owned binary directory, cleared both local
archive overrides and ran the actual worker `setup -models smollm2:135m`.
The official HTTPS install published a new ownership-marked CLI, retained the
model manifest and stopped its owned API on completion. A subsequent installed
setup returned native process exit 0 and `Setup complete`. The first PowerShell
wrapper returned an empty `ExitCode` property after manual `WaitForExit`; the
check was corrected to `Start-Process -Wait -PassThru`, without changing the
installer. The repeatable ownership-guarded command is in
[Windows worker](../windows-worker.md#opt-in-real-native-acceptance).

Native SCM acceptance used the actual worker executable under one uniquely
named manual LocalSystem test service, an inert explicit loopback Ollama and a
real DUMB queue. It verified SCM Running, two generation jobs across two SCM
starts, cancellation/goodbye/process exit after Stop, and deletion confirmed
by `ERROR_SERVICE_DOES_NOT_EXIST`. No existing service configuration changed.

Signed update acceptance built real GUI-subsystem workers with throwaway
minisign keys and inert loopback release/server fixtures. It verified
0.1.0 → HTTP 426 → signed 0.2.0 → exit 4 → native process restart → hello → commit;
a wrong-key signature preserved the running binary byte-for-byte; three failed
probation starts triggered mapped-executable rollback, old-worker restart and
bad-version skipping. The restored worker removes a formerly locked `.bad`
image on startup. No production signing key or published release was involved.

The absent-endpoint real test exposed a platform bug: Windows
`syscall.ECONNREFUSED` is a synthetic compatibility errno, while actual ConnectEx
refusal is Winsock 10061. The native regression reproduced it before correction.
The platform classifier now recognizes that exact native error, including the
default 11434 endpoint, and rejects resets/timeouts/access errors as evidence of
absence. Failed initial probing did not publish or modify an installation.

Final native six-package tests passed again (0.392/0.220/1.754/1.080/0.954/0.630s
for menubar/service/worker/update/Ollama/worker-command), the native owned-tree
cleanup test passed in 0.24s, and final GUI worker/tray builds passed. The complete
macOS build gate passed with the final production semantics. Final inspection
found no owned Ollama process, default API listener or test service. The owned
portable installation and small model remain available for development.

Actual real-worker tray startup/install/recovery/update behavior in RDP has not
been visually recorded; only the earlier manually run inert tray fixture has
visual/control evidence. Reboot persistence, official GUI installer behavior,
production signed release publication and a separate automatic Ollama version
updater are not claimed. Raw vendor logs contain local paths and identity
diagnostics and are not copied into the public repository; the text evidence
above keeps only allowlisted test-result lines.


## Combined native SCM signed-update acceptance

The remaining combined test passed using the current public source snapshot and
`windows-worker-e2e.ps1 -SCMUpdate`. Its Windows PowerShell parser check reported
no errors. No production source change was needed in this run. The first earlier
attempt failed because the fixture expected an old-worker hello before recovery;
the worker correctly performs a credential/version `GET /v1/workers/me` first.
The corrected fixture observes that old-version upgrade check and still requires
an actual new-version hello before accepting the update.

Two independently named manual LocalSystem fixture services used real
GUI-subsystem worker binaries, throwaway signing keys and loopback backend,
release and Ollama fixtures. The test queried the production recovery actions
(0/5/60-second restarts) and non-crash recovery flag. It verified the old mapped
worker exits with code 4 after signed replacement; SCM automatically restarts a
different PID which performs a 0.2.0 hello and commits the update. Terminating
only that service's exact process handle with code 137 then exercises the
second recovery action and another automatic restart/hello. Explicit Stop
reports zero Windows/service-specific codes and remains stopped while the
wrong-signature case runs. The other service rejects a wrong-key signature at
startup and minimum-version retry, preserving its PID and executable bytes.
Both workers report zero visible owned windows; both exact fixture services
are deleted and confirmed absent.

```powershell
. ./scripts/windows-dev-env.ps1 -Offline
./qa-automation/scripts/windows-worker-e2e.ps1 -SCMUpdate
```

The test passed in 11.56 seconds (package 12.007 seconds). Post-run read-only
inspection found zero QA services, default API listeners and owned Ollama
processes. Only allowlisted test results are retained in the linked UTF-8/LF
evidence. The existing unrelated green gates were not repeated because this run
changed evidence and task records only. Published release rollout, real-worker
RDP lifecycle visuals and reboot persistence remain separate unverified checks.
