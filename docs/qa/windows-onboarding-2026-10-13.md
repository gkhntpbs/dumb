# Windows onboarding verification — 2026-10-13

The Windows GUI worker now supports a visible setup console only for first-run
onboarding. Runtime tray/service/owned subprocesses remain console-free. The
self-contained executable includes the native tray implementation and installs
a protected byte-identical supervisor copy so the supervisor does not keep the
signed updater's previous stable image mapped.

## Native results

The focused Windows desktop tests passed in 2.354 seconds:

- Existing bearer-token identity contract: HTTP 200 succeeds; 401, 426 and
  redirects fail. Errors do not expose the token and redirects are not followed.
- Configuration/token persistence uses the restricted current-user, SYSTEM and
  Administrators ACL; an existing installation or credential is never overwritten.
- A copied resident supervisor restarts an actual mapped fixture executable only
  after exit 4, requires fresh readiness, rejects a duplicate supervisor and
  stops only its own child. Native deletion of `.prev` succeeds while the
  separate supervisor copy remains alive.
- Nonce path traversal is rejected before readiness file access.

Native affected regressions passed: menubar 0.433s, worker command 2.492s and
Ollama 0.938s. The current standalone GUI worker/tray compatibility build passed
with version 0.1.0. The full existing macOS build gate also passed after the
shared tray/progress/ready-hook changes. No production signing key, pairing
endpoint, publication, push, reboot or automated RDP interaction was involved.

The manual local backend helper passed native PowerShell parsing, bounded
worker identity authentication, owned backend cleanup, process-environment
restoration and restricted token-directory checks. It never prints token
contents. Run it manually from the desktop using
`./scripts/windows-onboarding-server.ps1`; it supplies a temporary loopback URL
and private token-file path for testing the existing token-based setup flow.
This is a fixture backend, not a deployed production server.

The allowlisted UTF-8/LF results are in
[windows-onboarding-tests-2026-10-13.txt](windows-onboarding-tests-2026-10-13.txt).
Postflight found zero QA services, default Ollama listeners and owned fixture
processes. Existing VM/services and the retained real Ollama model were preserved.

```powershell
. ./scripts/windows-dev-env.ps1 -Offline
go test -count=1 -v -timeout 2m -run TestDesktop ./cmd/dumb-worker
./scripts/build-windows-worker.ps1 -Version 0.1.0
```

## Earlier non-interactive limitation

The opt-in actual self-contained tray test was attempted twice over native SSH.
It failed after 32.74 and 32.66 seconds without a worker hello. Protected local
runtime diagnostics confirmed `desktop status UI unavailable`; the native
notification area is unavailable in that non-interactive SSH session. The
worker correctly fails closed. No third tray patch or automatic RDP launch was
attempted. The opt-in test is skipped in the ordinary native suite:

```powershell
$env:QA_WINDOWS_DESKTOP='1'
go test -count=1 -v -timeout 2m -run TestDesktopActualSingleExecutableTraySurvivesLauncherExit ./cmd/dumb-worker
```

This test uses random loopback backend/Ollama fixtures and no sibling tray
helper. On an interactive desktop it requires tray handshake, actual server
hello, a real queue fixture result after the launcher exits, then own shutdown.
It does not by itself prove the visible setup prompts or terminal-close UX.

## Authorized real RDP acceptance

Later explicit user authorization permitted computer control in the existing
RDP session and a uniquely named, transient same-user InteractiveToken QA task.
The final QA launcher uses the existing interactive user's normal token, no
password or newly granted privilege, and deletes only its exact own registration
once launched. Failed native token/session strategies were removed from the
final reusable launcher. No unrelated task, service, VM, network setting or
reboot was changed.

The actual version 0.1.0 GUI worker was launched visibly with the retained real
owned Ollama 0.40.3 installation and `smollm2:135m` model. This acceptance reused
the previously verified installation; it did not perform another fresh GUI
Ollama download/install. The first two attempts hit computer-input routing
issues: Turkish keyboard mapping changed URL punctuation and remote paste sent
a literal `v`. The worker correctly rejected both malformed URLs. No production
patch was needed.

Exact same-user console input then supplied Unicode fixture URL, the local
fixture bearer token at the echo-disabled hidden prompt, and the model name.
Every prompt was verified before input; token contents were never printed.
The helper reads the attached console through explicit UTF-16 buffers and the
native character count because the earlier managed StringBuilder read included
trailing characters. A bounded transient broker ran in the existing interactive
session; all exact own QA tasks were deleted afterward.

The [setup console](windows-onboarding-rdp-2026-10-13/setup-success.png) shows
credential/version validation, retained CLI detection/start, model ready, tray
worker readiness, actual per-user startup registration and the final terminal
can-close message. The image contains only this console's client rectangle;
the token is hidden and no unrelated desktop or title bar is retained.

After Enter closed the setup terminal, read-only process checks confirmed that
its process was gone while the detached supervisor, real worker and same-exe
tray remained in the interactive session. The user-visible
[native tray menu](windows-onboarding-rdp-2026-10-13/tray-ready-after-console-close.png)
shows `DUMB: ready (0 active)`, Pause and Quit. Its screenshot is restricted to
the exact tray PID's visible native popup class, with no unrelated taskbar panel.
A capture-start focus race initially closed the popup; a bounded capture waited
while the existing UI reopened it. No broad desktop screenshot was retained.

A real DUMB queue job then generated a nonempty result through the retained
Ollama after the setup terminal was closed. The first eight-token request used
a 10-second deadline and expired during cold generation. A bounded repeat with
the same eight-token budget and a 60-second deadline completed in 13.288 seconds.
The token and generated text are not included in public evidence. This result
proves runtime survival and real generation after terminal closure.

The actual `DUMB Worker` HKCU Run entry points to the stable installed executable
and `desktop` mode. Sign-in/reboot persistence remains unverified; no reboot was
performed. The current development executable has no embedded release signing
key, so this run does not claim production signed-release distribution. Earlier
native signed-update fixtures remain separate evidence.

The worker, tray, owned Ollama and fixture backend were intentionally left
running for user inspection. Transient launch/input/capture tasks were removed.
Private fixture token files and raw receipts are kept outside the public
repository; public evidence preserves screenshots and allowlisted outcomes only.
A later inventory found that the earlier resident processes were absent. Their
restricted log contained successful hellos and results but no shutdown or UI
failure message; the historical exit cause is unresolved. It is not attributed
to a manual Quit, tray failure, or task lifetime without supporting evidence.

Saved desktop configuration now preserves explicitly selected owned data root,
model directory, and loopback endpoint. Relative model directories retain the
original setup working-directory selection. An explicit default endpoint
remains external. Native regressions unset the setup environment before loading
configuration and confirm that the saved model directory reaches the actual
managed-child launch seam. Legacy defaults and explicit launch precedence are
covered. Test credentials use the existing restricted directory ACL, rather
than bypassing the native token permission check.

The test installation's executable and configuration were backed up privately.
Only this accepted fixture's configuration was migrated to the known retained
owned root. A clean-environment native relaunch reused that CLI, confirmed hello
and its resident roles beyond 149 seconds, then completed another real
eight-token job in 1.165 seconds. Read-only inspection found that these
processes belonged to a Windows job, but survival beyond the transient task's
60-second deadline disproves an immediate deadline explanation for this run.

The final startup-command route was then tested with all original Ollama
overrides explicitly removed. An exact own stop event was signaled in its
existing interactive session; the worker logged a graceful stop and its owned
model process tree disappeared. The latest development executable was copied
only after backup and verified against the build SHA256. This was a manual
development-fixture replacement, not a production signed release. The existing
startup guard routes the stable `desktop` command through a protected supervisor
copy, and its native lifecycle regression verifies fresh readiness, exit-4
restart, previous-image deletion, and scoped stop. The final startup-command
relaunch confirmed a fresh hello without another setup console or sibling tray
helper. Its private supervisor and installed executable both matched the latest
build SHA256. At 106 seconds the same resident roles completed a real eight-token
queue job with nonempty output in 1.287 seconds. They and the fixture backend
were left running for the user; zero transient QA tasks remained. Only the
exact temporary QA registration was deleted.

Final affected native regressions passed: worker command 4.172s, Ollama 0.872s
and menubar 0.421s. The final macOS build gate passed after all shared production
changes. Startup-command acceptance is separate from actual sign-in/reboot;
those remain unverified.

Reusable scoped QA sources are `scripts/windows-interactive-launch.ps1`,
`scripts/windows-onboarding-console-input.ps1`,
`scripts/windows-onboarding-console-broker.ps1`,
`scripts/windows-onboarding-console-capture.ps1` and
`scripts/windows-onboarding-menu-capture.ps1`. The launch/broker sources require
explicit invocation; first-run production onboarding does not register QA tasks.
Their Windows PowerShell parser checks passed.
