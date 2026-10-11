# QA — DUMB core

| Script | Purpose |
|---|---|
| `scripts/unit-tests.sh` | Unit tests (`go test ./...`) |
| `scripts/clean-results.sh [--dry-run]` | Removes test output |
| `scripts/build-gate.sh` | clean → unit tests (`QA_STRICT=1`); the gate in front of every build |

No UI tests (qa=none). Unit tests live next to the code, Go-style (`*_test.go`).

## Rules

- A new test is a new file; the runner scripts are never edited by hand to include it.
- Credentials arrive through `QA_`-prefixed environment variables, never in files.
- `results/` is never committed.

## Known pitfalls

Added here as they are learned. The first place to look when diagnosing a red gate.

## CI

Not yet — `build-gate.sh` is the single entry point CI will run unchanged.
