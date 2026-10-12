#!/usr/bin/env bash
set -euo pipefail
CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LAB_TMP="$(mktemp -d "${TMPDIR:-/tmp}/dumb-recovery-lab.XXXXXX")"
cleanup() { rm -rf "$LAB_TMP"; }
trap cleanup EXIT
export GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 QA_OLLAMA=0 QA_LAUNCHD=0
mkdir -p "$LAB_TMP/build"
export GOTMPDIR="$LAB_TMP/build"
cd "$CORE"
for package in recoverylab ollama worker; do
  go test -c -o "$LAB_TMP/$package.test" "./internal/$package"
done
printf 'Dedicated lab scratch after compilation: '
du -sk "$LAB_TMP"
for package in recoverylab ollama worker; do
  printf '\nFresh serial recovery tests: %s\n' "$package"
  if [[ "$(uname -s)" == "Darwin" ]]; then
    /usr/bin/time -l "$LAB_TMP/$package.test" -test.run '^TestRecovery' -test.count=1 -test.parallel=1 -test.timeout=30s -test.v
  else
    "$LAB_TMP/$package.test" -test.run '^TestRecovery' -test.count=1 -test.parallel=1 -test.timeout=30s -test.v
  fi
done
printf '\nOnly the created lab scratch directory will be removed: %s\n' "$LAB_TMP"
