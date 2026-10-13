#!/usr/bin/env bash
# GATE — core. Everything MUST be green BEFORE an artefact is built.
# Order: 0) clean old output  1) unit tests  2) UI tests (none).
# Any red step exits non-zero → the caller (core-build) does NOT build.
# Partially green is not green: no tests / not configured / missing env → RED.
# Usage: ./build-gate.sh (no arguments)
set -uo pipefail

QA="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${1:-all}"
QA_TOOL="none"
UI_TARGETS=()
export QA_STRICT=1

echo "═══════════ 0. Clean old output ═══════════"
"$QA/scripts/clean-results.sh" || true

echo "═══════════ 1. Unit tests ═══════════"
"$QA/scripts/unit-tests.sh" "$TARGET"; urc=$?
if [[ $urc -ne 0 ]]; then
  [[ $urc -eq 3 ]] && echo "❌ GATE RED — unit tests are not configured." \
                   || echo "❌ GATE RED — unit tests failed."
  echo "No artefact will be built."; exit 1
fi

echo "═══════════ 1b. Admin authorization, listener and snapshot regressions ═══════════"
# Already part of go test ./..., run by name so an admin regression is reported on its own.
if ! (cd "$QA/.." && go test -count=1 -run 'Admin|TypesMatchSpec|PanelLifecycle' ./internal/server/... ./internal/protocol); then
  echo "❌ GATE RED — admin regressions failed."; echo "No artefact will be built."; exit 1
fi

echo "═══════════ 2. UI tests ($QA_TOOL) ═══════════"
gate=0
if [[ "$QA_TOOL" == "none" ]]; then
  echo "ℹ️  UI tests are out of scope (qa=none — a deliberate decision)."
else
  for t in "${UI_TARGETS[@]}"; do
    if [[ "$TARGET" != "all" && "$t" != "-" && "$t" != "$TARGET" ]]; then continue; fi
    if [[ "$t" == "-" ]]; then "$QA/scripts/run-all-tests.sh"; else "$QA/scripts/run-all-tests.sh" "$t"; fi
    rc=$?
    if [[ $rc -ne 0 ]]; then
      label="$t"; [[ "$t" == "-" ]] && label="$QA_TOOL"
      [[ $rc -eq 3 ]] && echo "❌ $label: UI tests are not configured" || echo "❌ $label: UI tests RED"
      gate=1
    fi
  done
fi

echo "═══════════════════════════════════════════════"
if [[ $gate -eq 0 ]]; then
  echo "✅ GATE GREEN ($TARGET) — OK to build."
else
  echo "❌ GATE RED ($TARGET) — no artefact will be built."
fi
exit $gate
