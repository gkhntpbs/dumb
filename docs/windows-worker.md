# Windows worker

The portable Windows worker runs in the signed-in user's desktop session.
`dumb-worker.exe` contains its native Win32 notification-area implementation;
it launches its own `internal-tray` mode over private stdio pipes. A separate
`dumb-tray.exe` remains available for compatibility and inert native previews,
but the desktop worker does not require it beside the executable. Both builds
use the Windows GUI subsystem. First-run onboarding opens a setup console;
normal desktop startup and the internal tray do not create a console or an
application window. Right-click the tray icon to see readiness and active jobs,
Pause, Resume and Quit. Pause drains active jobs before reaching paused; Quit
drains before exiting. Explorer restarts restore the notification icon.

The tray uses the same versioned, sanitized stdio protocol as the macOS menu
bar. It receives state, active count and error category, never credentials,
request text or server addresses. Failed UI stops claims; use `run -headless`
only when explicitly choosing a CLI/service workflow. The tray requires an
interactive desktop; an SSH or session-0 service cannot display it.

## Saved Ollama selections

First-run setup saves an explicitly selected `DUMB_OLLAMA_DATA_DIR`,
`OLLAMA_MODELS`, and `DUMB_OLLAMA` in restricted desktop configuration. The
optional fields are `ollama_data_dir`, `ollama_models_dir`, and `ollama_url`.
The owned data root must be absolute. A relative model directory is resolved
against the original setup working directory so a later launch keeps the same
model selection. The Ollama endpoint must remain loopback-only. An explicitly
selected default endpoint stays external; an implicit default stays eligible
for owned recovery. Later explicit launch overrides take precedence without
rewriting saved configuration. Legacy configurations keep the default owned
root and endpoint behavior.

Native environment-free startup-command acceptance reused the retained owned
installation and model, confirmed a private `supervisor-*.exe` image and a real
queue result, and remained alive beyond the supervisor's 60-second startup
window. Native copied-supervisor tests also verify exit-4 restart and deletion
of the previous stable image. Actual sign-in/reboot persistence is unverified;
no reboot was performed. Details are in
[Windows onboarding verification](qa/windows-onboarding-2026-10-13.md).

## Portable development

Use a new, isolated project directory on the Windows host. Install a portable
Go toolchain under that directory, verify the official SHA256, and set PATH,
GOCACHE and GOMODCACHE for the current PowerShell process only. Keep source,
artifacts and logs under this directory. Do not reboot, change existing
services, firewall rules, global PATH, existing Ollama or virtual machines.
Limit test/build concurrency with GOMAXPROCS=2 and GOFLAGS=-p=1 on shared hosts.

```powershell
. ./scripts/windows-dev-env.ps1
go test ./internal/service ./internal/menubar ./internal/worker ./internal/update ./internal/ollama ./cmd/dumb-worker
./scripts/build-windows-worker.ps1
```

Configure `DUMB_SERVER`, `DUMB_TOKEN_FILE` and `DUMB_MODELS` in the launching
process, or pass their equivalent flags. The admin panel shows the computer
name unless `DUMB_WORKER_NAME` / `-name` is set; desktop onboarding saves a
`DUMB_WORKER_NAME` present at setup into `config.json` (a launch-time value
still wins). Restrict the token ACL to the current
user, SYSTEM and Administrators. For explicit GUI `run` launches, redirect stderr to a local
file; only onboarding opens the setup terminal. Use an explicitly selected loopback
Ollama endpoint during development so recovery cannot install or start the
host's existing Ollama. Native UI checks must run in the signed-in desktop
session. `go run ./cmd/tray-preview -helper <path> -duration 120s` exercises
status and Pause/Resume/Quit with inert state; it never contacts the network,
starts a worker, installs Ollama or changes services. `-log <file>` records
sanitized status/control evidence for GUI-subsystem previews. To capture native
startup focus/window metrics and cropped notification-area/menu screenshots,
run `scripts/windows-tray-evidence.ps1` manually in the interactive desktop;
`-OpenMenu` is an explicit QA action after measuring startup focus. The script
finishes within its chosen duration and never registers a scheduled task.

## Manual setup against a local test server

Open PowerShell in the isolated checkout and explicitly start the backend:

```powershell
./scripts/windows-onboarding-server.ps1
```

Keep that terminal open. The script builds and starts only the local server on
a free loopback port, validates its worker credential, and prints the server URL
and worker token file path. It creates fresh cryptographic worker/caller tokens
in a restricted directory under the development root's `evidence` directory,
outside the source checkout. The server consumes their SHA256 hashes. Token
contents are never printed by the helper. Read the worker token file privately
and paste its contents into the hidden setup prompt.

Then double-click `dist\windows\dumb-worker.exe` in Explorer. Supply the printed
server URL, the worker token, and the model names. The backend helper does not
launch the worker, a tray, a scheduled task, or an installer. Ctrl+C stops only
the backend it started and restores the helper's process environment. Retain
the restricted token directory only while that local test configuration is
needed. Each new backend invocation creates new credentials and a new port;
it does not reuse an earlier installed worker's configuration.

For bounded backend readiness and cleanup acceptance, run the helper with
`-DurationSeconds 1`. This does not perform interactive worker acceptance.

## Windows services

`service print` generates a headless SCM service command. The worker implements
SCM start, interrogation, stop and shutdown. Stop cancels its own worker context
and reports progress while graceful shutdown completes. Nonzero exits,
including self-update exit 4, are service-specific failures; `failureflag 1`
enables configured restart actions after these exits. Windows services run in
session 0 and deliberately do not display a tray.

`service install` requires administrator permission and is an explicit system
change. Its default service identity is LocalSystem; production use should
provision a dedicated low-privilege identity, token ACL and writable binary
location rather than run a desktop worker as LocalSystem. The desktop tray
and a service must not serve the same worker credentials simultaneously.

Unit tests simulate SCM controls and service commands without registration.
Actual service registration/start/stop/restart/removal is verified by the
opt-in native acceptance harness; reboot persistence remains a separate check. No reboot is required for development. Self-update uses the existing
signed archive verification, same-directory replacement and probation rollback;
Windows locked-file behavior must also pass on the target OS.

## Automatic owned Ollama installation and recovery

For the implicit default `http://127.0.0.1:11434`, `run` and `setup` now share
Windows installation/start/model-pull/recovery. Actual Winsock refusal (10061)
establishes an absent endpoint; a timeout, reset, access denial or occupied API
never authorizes installation over it. An explicit `-ollama`/`DUMB_OLLAMA` or an
already running API remains external. Its process and existing models are
preserved. Missing configured models may be pulled through its reachable API.

The worker downloads the [official standalone Windows ZIP](https://docs.ollama.com/windows#standalone-cli)
over verified HTTPS, validates it and extracts the full CLI/dependency package
into a new staging directory. Windows path aliases, traversal, duplicate names,
symlinks/devices and archive bombs are rejected. A regular `MZ` executable is
required before the new directory is published. Existing installations are
never replaced. The ZIP approach avoids the GUI installer, PATH changes,
autostart registration, services and reboot requests.

The default owned root is `%LOCALAPPDATA%\DUMB`; the portable binary directory
is `Ollama`, models default to `models`, and the owned child receives an isolated
`USERPROFILE` under `home` for its local identity/configuration. A configured
`OLLAMA_MODELS` is preserved. `DUMB_OLLAMA_DATA_DIR` selects another absolute
owned root. These settings affect the child/current launching process only.
Windows Job Objects keep owned model subprocesses with the owned Ollama; normal
shutdown, owned-process crash and even forced worker termination close the job
and remove only that process tree. Unknown endpoints/PIDs are never adopted.

`DUMB_OLLAMA_INSTALL_ARCHIVE` optionally supplies a securely relayed official
ZIP when direct download is unavailable. `DUMB_OLLAMA_INSTALL_SHA256` is required
and must match a digest obtained through the trusted download channel. The
installer still validates, stages and publishes the actual package; a digest
mismatch does not publish an installation. Normal automatic installation uses
the official HTTPS source without either override. This does not add an
automatic Ollama version updater; signed worker updates remain a separate flow.

## Opt-in real native acceptance

After dot-sourcing the isolated environment script:

```powershell
./qa-automation/scripts/windows-worker-e2e.ps1 -Update -SCM
./qa-automation/scripts/windows-worker-e2e.ps1 -SCMUpdate
./qa-automation/scripts/windows-worker-e2e.ps1 -Install -OllamaDataDir 'C:\path\to\isolated-owned-root'
./qa-automation/scripts/windows-worker-e2e.ps1 -InstalledRun -OllamaDataDir 'C:\path\to\isolated-owned-root'
```

`-SCM` and `-SCMUpdate` require administrator permission. `-SCMUpdate` explicitly
selects signed-update acceptance under two uniquely named test services with the
production recovery policy; `-Update` alone never registers a service. `-SCM` creates,
starts, stops, restarts and
removes one uniquely named manual test service; it never changes an existing
service. `-Install` requires no existing installation/default listener and
performs real download, bounded tiny-model pull, CPU-only generation, owned
crash/recovery and model-preserving binary uninstall/reinstall. `-InstalledRun`
runs the actual GUI-subsystem worker headlessly against that installed model,
serves a real queue job and verifies forced worker-exit tree cleanup. The
fixtures use `smollm2:135m`, one CPU affinity, one parallel job, context 512,
eight generated tokens and `OLLAMA_NO_CLOUD=1`. No reboot occurs. Runtime logs
can contain local machine paths and identity diagnostics; public evidence keeps
only allowlisted test results.

To repeat the direct production HTTPS reinstall specifically, use an explicitly
owned test root, ensure 11434 has no listener, and run the following in the same
process. This intentionally removes only marked owned binaries, retaining models:

```powershell
$Owned = $env:DUMB_OLLAMA_DATA_DIR
if (-not [IO.Path]::IsPathRooted($Owned)) { throw 'An explicit absolute owned root is required' }
$BinaryDir = Join-Path $Owned 'Ollama'
if (-not (Test-Path (Join-Path $BinaryDir '.dumb-owned.json'))) { throw 'Ownership marker missing' }
if (Get-NetTCPConnection -LocalPort 11434 -State Listen -ErrorAction SilentlyContinue) { throw 'Existing endpoint must be preserved' }
Remove-Item -LiteralPath $BinaryDir -Recurse -Force
Remove-Item Env:DUMB_OLLAMA_INSTALL_ARCHIVE -ErrorAction SilentlyContinue
Remove-Item Env:DUMB_OLLAMA_INSTALL_SHA256 -ErrorAction SilentlyContinue
$env:OLLAMA_MODELS = Join-Path $Owned 'models'
$env:OLLAMA_NO_CLOUD = '1'
$env:OLLAMA_NUM_PARALLEL = '1'
$env:OLLAMA_MAX_LOADED_MODELS = '1'
$env:OLLAMA_CONTEXT_LENGTH = '512'
$Process = Start-Process -Wait -PassThru -FilePath (Resolve-Path './dist/windows/dumb-worker.exe').Path `
  -ArgumentList @('setup','-models','smollm2:135m') `
  -RedirectStandardOutput (Join-Path $Owned 'direct-setup.stdout.log') `
  -RedirectStandardError (Join-Path $Owned 'direct-setup.stderr.log')
if ($Process.ExitCode -ne 0) { throw 'Direct HTTPS setup failed' }
if (-not (Test-Path (Join-Path $BinaryDir '.dumb-owned.json'))) { throw 'Installation was not published' }
if (Get-NetTCPConnection -LocalPort 11434 -State Listen -ErrorAction SilentlyContinue) { throw 'Owned setup API did not stop' }
```

Verified native results and the distinction between manual tray fixture and
actual lifecycle checks are recorded in
[Windows QA evidence](qa/windows-worker-2026-10-12.md).


## First-run desktop onboarding

Double-click the self-contained `dumb-worker.exe`. With no saved desktop
configuration, Windows allocates a visible setup console. Enter the server URL,
a pre-issued worker bearer token (input is hidden), and the comma-separated
Ollama models. No production URL or token is embedded in the public executable.
The existing `GET /v1/workers/me` contract validates the credential and worker
version before Ollama installation or configuration changes. Redirects are not
followed; rejected credentials and outdated workers stop setup.

The console reports Ollama probing, detection, official download/installation,
owned startup and configured-model pull progress. It preserves the existing
external-endpoint ownership rules. The installed executable, configuration and
restricted token live under `%LOCALAPPDATA%\DUMB\Worker`; existing files are
not overwritten. Desktop configuration supplies server, token path and models;
other existing CLI environment settings remain available. Only this user's
`DUMB Worker` HKCU Run value is registered, pointing at the stable installed
executable. An entry owned by another installation fails without replacement.

Setup starts a detached resident supervisor from a protected, byte-identical
private copy of the installed executable. The supervisor runs the current
stable worker image, which starts its native tray through the same executable.
It restarts only for signed-update/rollback exit 4 and does not restart after
Quit. Mapping the private supervisor copy keeps a previous stable worker image
free for deletion after an update. Setup accepts only a fresh nonce confirmation
following the actual worker hello and successful tray handshake. It then reports
that all checks passed and the setup terminal can close; the tray worker does
not depend on that console. Failed startup/handoff signals only the owned
supervisor/worker to stop and removes this attempt's installed files. Local
runtime diagnostics are retained in the restricted `worker.log`, not the public
repository.

Later double-clicks and sign-in startup launch the saved desktop configuration
without a setup console. A per-installation mutex prevents duplicate workers.
Services and explicit CLI commands retain their separate behavior. This flow
uses an existing worker token; new-worker production pairing is deferred to the
private implementation and is not invented by this UI.

Implementation and native checks are recorded in
[Windows onboarding verification](qa/windows-onboarding-2026-10-13.md).
Authorized actual RDP acceptance now verifies the setup console, hidden token
input, actual per-user startup registration, tray survival after terminal close
and a real small-model queue result. Sign-in/reboot persistence remains separate.
The scoped QA screenshot sources and results are in the linked verification record.
