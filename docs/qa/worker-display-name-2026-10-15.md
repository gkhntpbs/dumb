# Worker display name acceptance — 2026-10-15

Admin worker cards showed only the random 43-character worker id. The contract
changed first in `docs/protocol/openapi.yaml`: an optional `worker_name` in
`HelloRequest` and `AdminWorker`; the TypeScript contract was regenerated with
`npm --prefix clients/typescript run generate`.

## Semantics

- Display only. Authentication, routing, leases and identity stay on the
  credential's `worker_id`; two workers may share a name.
- Worker: sends `os.Hostname()` by default; `-name` / `DUMB_WORKER_NAME`
  overrides it. An explicit name that is blank, has control characters or is
  longer than 64 characters is a usage error. `service install|print`,
  `dumb-worker install` and Windows desktop onboarding persist the name only
  when it was given (plist/unit `DUMB_WORKER_NAME`, Windows `binPath` `-name`,
  `config.json` `name`); otherwise the service keeps following the hostname.
- Server (`protocol.NormalizeWorkerName`): trims whitespace, keeps at most 64
  runes; invalid UTF-8, control (C0/C1, DEL), format (`Cf`, e.g. bidi overrides,
  zero-width) and line/paragraph separators make the name absent. Hello never
  fails because of the name. A later hello replaces or clears it.
- Compatibility: hello decoding ignores unknown fields (unchanged), so a new
  worker works against an older server, which simply does not show the name. An
  old worker without the field keeps working; its admin row has no
  `worker_name` and the panel falls back to the id. No `protocol.Version` bump.

## Tests

- `internal/protocol/workername_test.go`: trimming, 64-rune bound on multibyte
  text, control/format/separator/invalid UTF-8 rejection.
- `internal/server/worker_name_test.go`: stored and exposed per worker via
  `/admin/v1/workers`; raw-JSON old worker without the field; control
  characters dropped, not stored and not echoed; 200-rune name bounded; same
  name on two workers stays two identities; re-hello replaces/clears; claim
  keyed on the credential; hello with unknown future fields still accepted.
  Mutation check: storing the raw name turned the test red; restored.
- `internal/worker/name_test.go`: real server; the first hello carries the
  hostname by default and a configured name (trimmed); invalid names refused.
- `cmd/dumb-worker/name_test.go`, `install_name_test.go`: flag/env precedence
  and validation, `service print` carries only an explicit name, `install`
  writes it into the unit only when given, desktop config saves/restores it
  with launch-time precedence and rejects a tampered saved name.
- `internal/service/name_test.go`: quoting of `"`, `%`, `$`, `&`, `<` and
  non-ASCII in plist, systemd unit and Windows `binPath`.
- `internal/server/adminui/panel.test.mjs`: card title/secondary id/fallback,
  table columns, name search, details Name/Worker ID, markup-like names stay text.

## Visual verification

`cmd/admin-preview` was run on `127.0.0.1:52148` (52147 was already in use by
another preview and was left alone) and stopped afterwards. Fixture workers have
random production-like ids: "Studio Mac mini", no name, a 64-character name
(offline) and "Living room Pi 5". `qa-automation/admin/worker-names.mjs`
(headless Chromium 1248, Playwright 1.62.1) asserted and captured:

| File | Checked |
|---|---|
| `worker-display-name-2026-10-15/device-cards.png` | Named cards show the name with the id below in small monospace; the nameless card shows the id on one line with an ellipsis and "No name reported"; the 64-character name wraps to two lines; every card child stays inside its card |
| `worker-display-name-2026-10-15/devices.png` | Whole Devices view at 1440 px |
| `worker-display-name-2026-10-15/workers-table.png` | Name and Worker ID columns; long name and ids ellipsized; table fits its panel without horizontal scroll; names that fit are not clipped by a pixel |
| `worker-display-name-2026-10-15/details-long-name.png` | Dialog shows Name (full 64 characters) and the full Worker ID |
| `worker-display-name-2026-10-15/details-nameless.png` | Dialog shows "Not reported" and the full Worker ID |
| `worker-display-name-2026-10-15/device-cards-mobile.png` | 390 px: no page overflow, titles at most two lines, ids ellipsized |

Reviewing the first screenshots found two issues, both fixed before the final run:
an unbreakable id used as a fallback title wrapped onto two lines and misaligned
the card row (now one line with an ellipsis), and the first table column clipped
the last glyph of a name ("Studio Mac mini": text 94.19 px in a 94 px box; now
2 px right padding). Adding the Name column made the table 9 px wider than its
panel; the id column is bounded to 130 px (the full id stays in the tooltip, the
card and the dialog).

## Commands (passed locally, go1.26.5 darwin/arm64, Node.js 22.23.2)

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 QA_OLLAMA=0 QA_LAUNCHD=0 ./qa-automation/scripts/build-gate.sh   # GATE GREEN
go test -race -count=1 ./internal/protocol ./internal/server/... ./internal/worker ./internal/service ./cmd/dumb-worker ./clients/typescript
go vet ./... && GOOS=windows go vet ./... && GOOS=darwin go vet ./... && GOOS=linux go vet ./... && test -z "$(gofmt -l .)"
GOOS=windows GOARCH=amd64 go build ./... && GOOS=linux GOARCH=arm64 go build ./...
npm --prefix clients/typescript run verify       # contract drift, build, 33 unit tests, package
QA_ADMIN_PASSWORD_FILE=<file> QA_ADMIN_LISTEN=127.0.0.1:0 ./scripts/admin-preview.sh
PLAYWRIGHT_MODULE=<playwright/index.mjs> CHROME_BIN=<chromium> QA_ADMIN_PASSWORD_FILE=<file> QA_ADMIN_URL=<printed URL> node qa-automation/admin/worker-names.mjs
```

## Not done

- No push, release or deployment; production keeps showing ids until a new
  server is deployed, and workers send names once they run this version.
- No Windows or Raspberry Pi run of the new flag; Windows paths are covered by
  cross-compiled vet and the `binPath` unit test only.
- `docs/admin-evidence/` screenshots were not refreshed and predate the name.
- `public-source-audit.sh` was not run (Gitleaks 8.30.0 not installed here).
