# Add worker / Re-pair acceptance — 2026-10-15

Operators created pairing codes with a private script. The admin panel now has
**Devices > Add worker** and **Re-pair worker** in a worker's details dialog,
built on the existing `POST /admin/v1/pairing`.

## Design

- **Capability signalling (contract first).** `GET /admin/v1/session` gains
  optional `role`, `credential_management` and `public_url`, present only for an
  authenticated session; an anonymous request still gets exactly
  `{"authenticated":false}`. Additive: older servers send only `authenticated`
  and the panel treats missing fields as "not available". Needed because,
  without management configured, the pairing endpoint is refused by the strict
  Origin check with 403 before it could answer 503
  (`TestAdminPairingWithoutManagementIsRefused`).
- **Worker server URL.** The panel is reached through an SSH tunnel, so
  `location.origin` is never used and no existing setting held the public URL
  (`AdminOrigin` is the tunnelled browser origin). Optional `Config.PublicURL`
  (reference server `DUMB_PUBLIC_URL`), validated by `New` as absolute https
  with a host and no user info/query/fragment, trailing slash removed. Unset:
  an editable "Server URL for workers" field (https only) whose value alone is
  kept in `localStorage` (`dumb.admin.workerServerUrl`).
- **Roles.** Management off: the button stays clickable (secondary style) and
  the dialog explains that pairing is turned off on the server. `admin-read`:
  disabled button and a note that adding workers needs `admin-manage`.
- **Commands.** Code never on argv or in shell history:
  - Linux: `install.sh` / `dumb-worker install -system ... -pair-code-stdin`
    (`-force-pair` for re-pair, keeps the identity);
  - macOS: `dumb-worker pair` (hidden prompt) into
    `~/Library/Application Support/DUMB/worker-token`, then `service install`;
    re-pair replaces it and `launchctl kickstart -k`;
  - Windows: `Read-Host -AsSecureString` piped into `dumb-worker.exe pair`
    (GUI-subsystem binary: no console prompt), then the token is handed to the
    existing first-run setup through the clipboard; re-pair replaces
    `%LOCALAPPDATA%\DUMB\Worker\token`, then Quit/start from the tray.
  Values are single-quoted for sh and PowerShell (including curly quotes);
  long commands use line continuations so they wrap between flags.
- **Security.** The code lives only in memory and one `textContent` node while
  the dialog is open; Close, Escape (`cancel` and `close`), expiry, sign-out,
  session loss and late responses erase it and stop the countdown. No
  `innerHTML`. One request per Generate even when double-clicked. The countdown
  uses the server `Date` header, so a skewed browser clock neither expires a
  live code nor extends it past the requested lifetime. 400/401/403/404/409/429/
  503 and network failures get plain messages; 401 re-checks the session.
- **Preview.** `cmd/admin-preview` has an inert fake pairing provider (random
  throwaway codes, exchange always refused, nothing stored) and an
  `admin-manage` fixture session. `QA_ADMIN_PAIRING=on|off|read-only`,
  optional `QA_ADMIN_PUBLIC_URL`.

## Tests

- `internal/server/admin_capabilities_test.go`: session fields for anonymous,
  admin-manage, admin-read, unconfigured management and worker role; the 403
  that motivates the flag; `NormalizePublicURL` and `New` validation.
- `cmd/dumb-server/public_url_test.go`: `DUMB_PUBLIC_URL` parsing.
- `cmd/admin-preview/pairing_test.go`: the fake provider is inert.
- `internal/server/adminui/panel.test.mjs` (27 tests, in the gate): gating,
  request shape and single request on repeated submit, textContent rendering,
  command contents and quoting, copy, countdown, expiry, erase on
  close/cancel/logout/late response, each error status, re-pair `worker_id`,
  server URL validation/storage, clock skew. Mutation checks: removing the busy
  guard, not clearing the code text on reset, breaking sh quoting, or ignoring
  the server `Date` header each turned a test red.

## Visual verification

Two previews on free loopback ports (52147 and another existing preview were
left alone): `QA_ADMIN_PAIRING=on QA_ADMIN_PUBLIC_URL=https://dumb.example.com`
and `QA_ADMIN_PAIRING=off`, both stopped afterwards.
`qa-automation/admin/add-worker.mjs` (headless Chromium 1248, Playwright 1.62.1,
Playwright clock for expiry) asserted the behaviour and captured:

| File | Checked |
|---|---|
| `add-worker-2026-10-15/devices.png` | Devices view at 1440 px: "+ Add worker" aligned right of the "Connected devices" heading, no note when enabled |
| `add-worker-2026-10-15/dialog-empty.png` | Empty dialog: two-column fields, defaults 10 minutes / 1 year, configured public URL shown as text, no overflow |
| `add-worker-2026-10-15/generated-linux.png` | Code + Copy code, "Expires in 9:57", worker ID, Linux tab: curl + `sudo sh install.sh -system ... -pair-code-stdin` split per flag, per-step Copy |
| `add-worker-2026-10-15/generated-macos.png` | `pair` then `service install` with the token path quoted |
| `add-worker-2026-10-15/generated-windows.png` | Masked `Read-Host` piped into `pair`, setup hand-off with `DUMB_WORKER_NAME` |
| `add-worker-2026-10-15/expired.png` | "Code expired and removed from this page", Copy code disabled, red expired line, "Generate a new code" |
| `add-worker-2026-10-15/repair.png` | "Re-pair Living room Pi 5", same worker ID as the details, `-force-pair` command with the worker's models |
| `add-worker-2026-10-15/server-url-fallback.png` | Session without `public_url`: editable field, normalized `https://workers.example.org` in the commands |
| `add-worker-2026-10-15/disabled-devices.png` | Management off: secondary button with "Pairing is turned off on this server" under it |
| `add-worker-2026-10-15/disabled-dialog.png` | Explanation panel, no form, no request sent |
| `add-worker-2026-10-15/mobile-dialog.png`, `mobile-dialog-windows.png` | 390 px: one-column fields, stacked code box, full-width tabs, no horizontal overflow in page, dialog or command blocks |
| `add-worker-2026-10-15/mobile-devices.png` | 390 px Devices panel: button inside the viewport next to the heading |

Also asserted: one POST with the page Origin and JSON for a double click; the
countdown moves; clipboard equals the code and each command; no command, no
DOM node, no storage and no console line contains the code after close, Escape
or expiry; arrow keys switch tabs; `localStorage` holds only the URL.

Review of the first screenshots found and fixed: flags wrapped mid-token
(`-` / `models`), now explicit line continuations; title and intro touching;
an empty code box after expiry, now labelled; the disabled note crowding the
cards; the countdown expired immediately when the browser clock was ahead of
the server (now based on the server `Date` header); Escape left the code in
the DOM until the asynchronous `close` event (now also cleared on `cancel`).
The existing `browser.mjs` and `worker-names.mjs` acceptances still pass
against the new preview (their screenshots were not re-committed).

## Commands (passed locally, go1.26.5 darwin/arm64, Node.js 22.23.2)

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 QA_OLLAMA=0 QA_LAUNCHD=0 ./qa-automation/scripts/build-gate.sh   # GATE GREEN
GOMAXPROCS=2 GOFLAGS=-p=1 go test -race -count=1 ./internal/server/... ./internal/protocol ./cmd/dumb-server ./cmd/admin-preview
GOOS=linux go vet ./... && GOOS=darwin go vet ./... && GOOS=windows go vet ./... && test -z "$(gofmt -l .)"
npm --prefix clients/typescript run verify
QA_ADMIN_PASSWORD_FILE=<file> QA_ADMIN_LISTEN=127.0.0.1:0 QA_ADMIN_PAIRING=on QA_ADMIN_PUBLIC_URL=https://dumb.example.com ./scripts/admin-preview.sh
QA_ADMIN_PASSWORD_FILE=<file> QA_ADMIN_LISTEN=127.0.0.1:0 QA_ADMIN_PAIRING=off ./scripts/admin-preview.sh
PLAYWRIGHT_MODULE=<playwright/index.mjs> CHROME_BIN=<chromium> QA_ADMIN_PASSWORD_FILE=<file> QA_ADMIN_URL=<on URL> QA_ADMIN_URL_OFF=<off URL> node qa-automation/admin/add-worker.mjs
```

## Not done

- No push, release or deployment. Production shows the button only after a new
  server is deployed; the private embedding application must set
  `Config.PublicURL` (otherwise operators type the URL once per browser).
- The Windows and macOS commands were not run on a real host (no access to
  the Windows machine in this task). Open questions recorded in `TODO.md`:
  whether PowerShell waits for and pipes stdin into the GUI-subsystem `pair`,
  and the clipboard hand-off to first-run setup. The Linux command matches
  the `install.sh` flow already accepted on a Raspberry Pi
  (`pi-system-install-2026-10-15.md`). sh quoting was checked with sh, bash and
  zsh; PowerShell quoting was not executed (no `pwsh` here).
- The Windows first-run setup still takes a token, not a pairing code.
- `public-source-audit.sh` was not run (Gitleaks is not installed here). The
  codes in the screenshots are random preview values that no server accepts.
