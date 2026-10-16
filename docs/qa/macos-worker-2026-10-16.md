# macOS worker application acceptance, 2026-10-16

## Implemented behavior

The Finder application starts native pairing-code or worker-token setup, writes
private configuration and credentials, optionally registers its own user launch
agent, and starts the real readiness-driven menu worker. A process lock prevents
duplicate desktop supervisors. Setup retries after an issued pairing credential
retain that credential and never redeem the code twice. The standalone CLI
retains its signed binary updater. The application requires whole-app replacement
because replacing only its Go executable would invalidate its bundle signature.

## Verification

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 ./qa-automation/scripts/build-gate.sh
go vet ./...
go test -count=1 -run 'MacDesktop|MacApplication' ./cmd/dumb-worker
go test -count=1 -run TestNativeHelperProtocol ./internal/menubar
QA_MACOS_APP="$PWD/dist/DUMB Worker.app" go test -count=1 -run TestMacDesktopApplicationEndToEnd ./cmd/dumb-worker
DUMB_BUILD_VERSION=0.3.3 DUMB_BUILD_ARCH=arm64 ./scripts/build-macos-app.sh
DUMB_BUILD_VERSION=0.3.3 DUMB_BUILD_ARCH=amd64 ./scripts/build-macos-app.sh
```

The full local gate and vet passed. Inert TLS and injected dialog fixtures verify
identity status and shape, HTTPS-only setup, restricted configuration and token
permissions, symlink refusal, no overwrite, one pairing redemption across
connection/save retries, and preservation of desktop code-signing integrity by
disabling binary-only updates. Native Swift subprocess tests verify protocol
states, input EOF and 16 KiB boundaries. The existing sealed recovery/queue tests
cover actual jobs and menu lifecycle without contacting host Ollama.

Both architecture application bundles and ZIPs passed ad-hoc `codesign --verify
--deep --strict`, plist versions, ZIP integrity and executable architecture checks
in local packaging dry runs. Published artifacts require a fresh clean-source
build with the public updater key and signed release checksums.

Native launch investigation reproduced an initialization defect: direct modal
startup and `finishLaunching` alone left `NSApplication.isRunning` and
`NSRunningApplication.current.isFinishedLaunching` false. Running the standard
application event loop and presenting the dialog after
`applicationDidFinishLaunching` made both true. Setup, retry and error dialogs now
share that lifecycle. The GUI-opt-in helper command `--lifecycle-self-test` passed
outside the GUI-restricted sandbox. Offline input tests remain in the default
gate and do not initialize AppKit.

After the lifecycle correction, CUA attached to the inert native setup fixture
and verified the actual server URL, models, authentication selector, secure
credential field, start-at-login option and Connect/Cancel controls. The captured
form is [macos-worker-setup-2026-10-16.png](macos-worker-setup-2026-10-16.png).
The fixture uses a generic probe bundle icon; the distributed app retains the
existing official icon resources. Cancel was exercised through accessibility and the owned probe exited with
code 2, confirming cancellation and scoped cleanup.

The actual newly built application also passed its opt-in end-to-end test in
0.59 seconds: temporary private HOME/configuration, real supervisor and native
helper handshake, actual queue hello, a nonempty job result from the inert
explicit Ollama endpoint, duplicate-launch exclusion, scoped termination and
worker goodbye. No host Ollama process, installer or model store was used.

## Remaining acceptance

No real clean-machine Ollama install, host model download, launchd registration,
login/reboot persistence or quarantined-release Gatekeeper acceptance was run.
No Apple Developer ID identity is available; ad-hoc signing and the minisign
release signature have different purposes. Live server-to-worker inference on
this newly packaged application remains a separate acceptance step.
