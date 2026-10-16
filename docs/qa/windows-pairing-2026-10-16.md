# Native wizard pairing verification — 2026-10-16

The revised Windows first-run wizard accepts a one-time pairing code by default,
exchanges it for a worker credential, then checks that credential. A pre-issued
worker token remains available through the explicit T selection.

## Regression and local gate

The implementation task first reproduced the original defect with a regression
that failed with `pairing code used as bearer credential`. The corrected tests
verify that the exchange happens before the bearer credential check, the code
never reaches the bearer check, and credentials do not appear in wizard output.
A failed exchange requires explicit retry and a newly entered code; it does not
silently reuse the first code. An HTTPS fixture exercises the actual exchange
transport, including the pairing request and response.

The implementation agent reported successful macOS `go vet ./...`, focused
setup/pairing tests and the complete `./qa-automation/scripts/build-gate.sh`
(`GREEN (all)`). The full gate preceded a tests-only change that explicitly clears
ambient `DUMB_SERVER` in scripted setup cases; the focused tests passed again
after that isolation change. Initial sandbox listener restrictions were resolved
with approved execution, not by changing TLS or fixture behavior. Windows amd64
cross-compilation of worker tests and executable also passed.

## Native Windows execution

A separate source snapshot was copied to an isolated development directory and
executed with portable Go 1.26.5 on Windows 11 x64. It included the final wizard
implementation and regression cases, before the final tests-only ambient-server
isolation. Module downloads were disabled, and existing isolated module/build
caches were used. Go concurrency remained limited to two processors and one
package. No global PATH, trust-store or user environment setting changed.

```powershell
. ./scripts/windows-dev-env.ps1 -ToolchainRoot '<portable Go root>' -Offline
go test -count=1 -timeout 3m -run 'TestSetup|TestPair' -v ./cmd/dumb-worker
go build -ldflags '-H=windowsgui' -o './pairing-preview.exe' ./cmd/dumb-worker
```

The native tests passed, exit 0, package test time 0.369 seconds. Coverage includes:

- Pairing exchange before credential verification and explicit fresh-code recovery.
- Real HTTPS pairing and CLI fixtures, redirect refusal, bounded responses and failure statuses.
- Atomic credential replacement and secret-output checks.
- Existing-token setup, URL/authentication retries, exit behavior and confirmed defaults.
- Windows clipboard UTF-16 bounds, terminal banner and spinner behavior.

The separate GUI-subsystem preview executable also built successfully.

## Native visual acceptance

[The actual native wizard capture](windows-pairing-2026-10-16-auth-choice.png)
shows step 2 offering `Pairing code or worker token [P/T] (default P)`.
It was captured in the existing interactive Windows session through a uniquely
named temporary scheduled task. The task completed with result 0 and was removed.
The image was visually inspected after retrieval.

The preview used a new isolated `LOCALAPPDATA` and an inert loopback HTTPS URL.
The QA adapter confirmed the exact server prompt, pressed Enter to accept the
visible default, verified the authentication-choice text in the console buffer,
and cropped only the preview console. No pairing code or worker token was entered;
no authentication request, model operation, installation or startup registration
was reached. The adapter stopped only the process it had launched.

The reusable fixture is
[`qa-automation/windows/pairing-preview.ps1`](../../qa-automation/windows/pairing-preview.ps1).
Run it in an interactive Windows session with a separately built preview executable
and a new output directory:

```powershell
./qa-automation/windows/pairing-preview.ps1 `
  -Executable '<absolute path to pairing-preview.exe>' `
  -OutputRoot '<new isolated evidence directory>'
```

## Limits and activation

Existing installed worker, tray, Ollama, configuration and startup state were
preserved. No production request, deployment, release, push, reboot, firewall or
unrelated service change occurred. Native HTTPS fixture success and the visible
wizard choice do not establish live production onboarding with the rebuilt binary.

The currently published v0.3.1 Windows binary remains token-only. The new direct
pairing-code panel flow requires a rebuilt worker; this work has not activated
that change in production. Useful nonempty Qwen answers and thinking-budget
behavior are a separate issue and are not validated by this pairing report.
