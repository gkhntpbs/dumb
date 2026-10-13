# Admin browser acceptance — 2026-10-13

The dark, password-only, same-server control room was exercised in a real Chromium
browser at 1440px desktop and 390px mobile widths. The isolated preview creates
synthetic public worker/model/job metadata; it never contacts Ollama or user devices.
The stale node uses an injected freshness clock during registration only. No password,
session cookie, prompt or model output appears in these screenshots.

- `desktop.png`: overview with real browser-session samples and server-period outcomes.
- `devices.png`, `devices-mobile.png`: connected device cards, statuses, capacity and inventory.
- `queue-retention.png`, `queue-mobile.png`: queue summaries, lifecycle filters and timing/deadlines.
- `worker-details.png`, `job-details.png`: safe metadata inspection.
- `models.png`, `models-mobile.png`: model availability and shared capacity.
- `api.png`, `api-mobile.png`, `api-stale.png`: actual public HTTP counters, rolling traffic, status groups, endpoint timings and independently retained stale API observations.
- `login.png`, `denied.png`: password gate and rejected sign-in.
- `narrow.png`, `stale.png`, `empty.png`: responsive overview, retained stale observations and empty search.
- `legacy/`: preserved screenshots of the previous token-based interface; these do not describe current access.

## Repeat

Start `./scripts/admin-preview.sh` with a runtime `QA_ADMIN_PASSWORD` or
`QA_ADMIN_PASSWORD_FILE`; the default numeric loopback address is `127.0.0.1:52147`.
Set `QA_ADMIN_LISTEN=127.0.0.1:0` for an ephemeral port. Credentials are never printed.

Run `node qa-automation/admin/browser.mjs` with `PLAYWRIGHT_MODULE` pointing to an
installed Playwright ES module and `CHROME_BIN` to its installed Chromium executable,
plus the same runtime credential input and optional `QA_ADMIN_URL`. The test refreshes
these screenshots and checks rejected login, cookie session restoration after reload,
all menu pages, details/search, mobile overflow and sign-out visibility, chart creation,
stale data retention and server-side logout followed by reload. Run the script promptly
after starting the preview to see retained terminal jobs (60 seconds).

`node --test internal/server/adminui/panel.test.mjs` checks deterministic lifecycle,
metadata escaping including malicious model strings, filters, expiry cleanup and
bounded polling backoff. The full Go gate also executes these checks.

## Graph dependency

The local asset `internal/server/adminui/assets/chart.umd.js` is the unmodified
Chart.js 4.5.1 UMD distribution from the official npm `chart.js` package. Its
[MIT license](../../internal/server/adminui/CHART-JS-LICENSE.md) is included.
Upstream: https://www.chartjs.org/ and https://github.com/chartjs/Chart.js.
There are no CDN calls or new Go dependencies. Chart data comes only from authenticated
server snapshots. Session samples are bounded to 60, contain gaps after failed refreshes,
and reset on reload, logout, server epoch changes and model-filter changes. Accessible
text counts accompany the canvases. This is not persistent telemetry history.

## API measurements and dark theme

The API menu uses `GET /admin/v1/api` from the generated authoritative contract.
It shows process uptime, actual completed public requests, request rate, in-flight
requests, recent latency, unauthorized and rate-limited counts, and allowlisted
route templates. The rate window is shorter during the first five minutes of
uptime. Its two charts use server-produced 10-second buckets and recent response
status counts; no history is invented in the browser. The preview periodically
sends harmless requests through the actual public handler without opening a
public listener. Requests stay inside this fixture process.

API refresh failures keep the last API observation explicitly stale while other
views continue updating; authorization failures clear the entire session. All
pages, charts, dialogs and login use the shared dark palette. Body and section
labels use 14px, metadata 12px, headings 24px, and primary counters 32px, with
400/600 weights. Mobile acceptance covers all five menu pages without overflow.
