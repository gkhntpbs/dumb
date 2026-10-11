#!/usr/bin/env bash
# Unit tests — core. Commands come from unit_test_cmd in the workspace config;
# if they change, update both. Commands run from the repository root.
# Usage: ./unit-tests.sh (no arguments)
# Exit: 0 passed · 1 red · 3 not configured (counts as RED in the gate)
set -uo pipefail

PRODUCT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="${1:-all}"

# Parallel arrays (macOS bash 3.2 compatible — no associative arrays)
LABELS=('default')
CMDS=('go test ./...')

rc=0; ran=0
for i in "${!LABELS[@]}"; do
  label="${LABELS[$i]}"; cmd="${CMDS[$i]}"
  if [[ "$TARGET" != "all" && "$label" != "default" && "$label" != "$TARGET" ]]; then continue; fi
  ran=1
  if [[ -z "$cmd" ]]; then
    echo "⚠️  [$label] unit test command NOT CONFIGURED (workspace config → unit_test_cmd)."
    [[ $rc -eq 0 ]] && rc=3
    continue
  fi
  echo "▶ [$label] $cmd"
  if (cd "$PRODUCT_DIR" && bash -c "$cmd"); then
    echo "✅ [$label] unit tests passed"
  else
    echo "❌ [$label] unit tests RED"; rc=1
  fi
done
[[ $ran -eq 0 ]] && { echo "❌ '$TARGET' has no defined target."; exit 1; }
exit $rc
