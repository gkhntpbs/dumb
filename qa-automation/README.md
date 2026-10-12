# QA — DUMB core

| Script | Purpose |
|---|---|
| `scripts/unit-tests.sh` | Unit tests (`go test ./...`) |
| `scripts/clean-results.sh [--dry-run]` | Removes test output |
| `scripts/build-gate.sh` | clean → unit tests (`QA_STRICT=1`); the gate in front of every build |

No UI tests (qa=none). Unit tests live next to the code, Go-style (`*_test.go`).

The Go gate discovers `clients/typescript/qa_test.go`, which runs the caller package
contract check, build, Node unit tests, tarball consumer checks and an integration
against the actual Go server with local workers. Install its locked prerequisites
with `npm ci --prefix clients/typescript --ignore-scripts` first. Missing Node.js
or dev dependencies fail clearly; tests never fetch dependencies automatically.
CI pins Node.js 26.0.0/npm 11.12.1 and installs them before the gate.

Run caller-only checks with `npm --prefix clients/typescript run verify`; run the
real server fixture with `go test -count=1 -v ./clients/typescript`.

## Rules

- A new test is a new file; the runner scripts are never edited by hand to include it.
- Credentials arrive through `QA_`-prefixed environment variables, never in files.
- `results/` is never committed.

## Known pitfalls

Added here as they are learned. The first place to look when diagnosing a red gate.

## CI

`.github/workflows/ci.yaml` installs the locked caller tooling and runs
`build-gate.sh` unchanged, then cross-platform vet and formatting checks.
The remote CI run remains unverified; the complete gate runs locally.
