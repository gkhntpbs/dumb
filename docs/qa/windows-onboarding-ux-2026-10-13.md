# Windows onboarding UX verification — 2026-10-13

The setup console supports masked credential input, native clipboard input,
per-field retries, elapsed progress, seven numbered steps and a final summary.
Desktop readiness waits for both the actual worker hello and available models.
The [actual summary](windows-onboarding-ux-2026-10-13/setup-success.png) shows all
seven steps completed. Additional images show
[URL retry](windows-onboarding-ux-2026-10-13/same-window-url-retry.png),
[masked credential retry](windows-onboarding-ux-2026-10-13/masked-token-auth-retry.png),
[elapsed progress](windows-onboarding-ux-2026-10-13/download-elapsed.png) and
[official download progress](windows-onboarding-ux-2026-10-13/official-download-progress.png).

Final affected native regressions passed: worker command 4.222 seconds, Ollama
0.875 seconds and menubar 0.423 seconds. The full workspace gate passed.
The allowlisted results are in
[windows-onboarding-ux-2026-10-13.txt](windows-onboarding-ux-2026-10-13.txt).

The fresh-download attempt completed the actual official Ollama download and
model installation. Its final API check exposed premature readiness from the
authenticated `/me` response before the actual worker hello and available models;
that attempt was paused before successful acceptance. The readiness reproducer
and corrected `OnStatusReady` chain require the actual hello/model-ready status.
The successful fixed-pass acceptance reused the installed CLI and model.
An initial clipboard transient remains unexplained; later independent clipboard
readback and native input succeeded. Same-OS-thread handling was hardened; the
transient's cause is not inferred from that change.

At final delivery the earlier interactive session and its fixture backend were
absent. The final installed startup command was exercised in the current
same-user interactive session against the same restricted fixture credentials.
The worker announced the real retained model and completed an eight-token job
with a 60-second deadline in 1.4563818 seconds. The result was done and nonempty;
credentials and generated text are excluded from public evidence. This run
verifies actual runtime generation after the earlier setup process was absent.
It does not reconstruct the historical cause of the earlier session exit.

An earlier reset used a stale interactive-session guard and partially moved
fixture files before the locked runtime log prevented completion. The original
and archive locations were reconciled by executable, configuration, credential
and log hashes; all ten model hashes were preserved. No model loss was found.
The final reset used terminating guards before mutation.

The native local-session stop event then stopped the exact owned runtime.
Postflight confirmed no owned runtime process, no default Ollama listener and no
transient QA task. Only the exact owned startup registration was removed.
The default worker, CLI, model and child-home directories were moved into a
restricted private archive. All ten model files retained their SHA256 values.
The previously retained separate development Ollama installation was untouched.

The latest standalone GUI executable was copied to the actual user's Desktop
after backing up its previous fixture executable. Its SHA256 matches the build:
`4f0a3d40f5d06ab2f5357688a080944bbf831a80f58de57d20b4a434c44b2883`.
PE subsystem 2 was verified. The final Desktop copy was not launched and the
default installation directories remain absent for a fresh first-run experience.
The fixture backend authenticated the worker credential with HTTP 200 using
version 0.1.0. Its restricted guide and credential files remain outside this
public repository.

The scoped acceptance tools are `scripts/windows-interactive-launch.ps1`,
`scripts/windows-onboarding-console-broker.ps1`,
`scripts/windows-onboarding-console-input.ps1`,
`scripts/windows-onboarding-console-capture.ps1` and
`scripts/windows-onboarding-menu-capture.ps1`.

```powershell
. ./scripts/windows-dev-env.ps1 -Offline
go test -count=1 -v -timeout 2m ./cmd/dumb-worker ./internal/ollama ./internal/menubar
./scripts/build-windows-worker.ps1 -Version 0.1.0
```

Actual sign-in/reboot persistence remains untested. No production signed GitHub
release was published. No reboot, unrelated service, VM, firewall or network
configuration was changed.
