#!/usr/bin/env bash
set -euo pipefail
CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "$(uname -s)" != Darwin ]]; then
  printf 'The native menu preview requires macOS.\n' >&2
  exit 1
fi
export GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 GOPROXY=off GOSUMDB=off
APP="$CORE/dist/DUMB Worker.app"
HELPER="$APP/Contents/Resources/DUMBMenu.app/Contents/MacOS/dumb-menu"
MARKER=''
if [[ -e "$APP" || -L "$APP" ]]; then
  for candidate in "$APP/Contents/Resources/.dumb-local-build" "$APP/Contents/.dumb-local-build"; do
    if [[ ! -L "$candidate" && -f "$candidate" && "$(cat "$candidate")" == 'DUMB local macOS build v1' ]]; then
      MARKER="$candidate"
      break
    fi
  done
  if [[ -L "$APP" || -z "$MARKER" || -L "$HELPER" ]]; then
    printf 'Refusing an unowned or symlinked preview application.\n' >&2
    exit 1
  fi
fi
if [[ ! -x "$HELPER" || "$MARKER" == "$APP/Contents/.dumb-local-build" || "$CORE/macos/MenuHelper.swift" -nt "$HELPER" || "$CORE/macos/MenuInfo.plist" -nt "$HELPER" ]]; then
  "$CORE/scripts/build-macos-app.sh"
fi
PREVIEW="$CORE/dist/menu-preview"
mkdir -p "$PREVIEW"
cd "$CORE"
go test -c -o "$PREVIEW/menu-preview.test" ./cmd/dumb-worker
export QA_MENU_BAR_HELPER="$HELPER"
unset QA_MENU_DIAGNOSTICS
export QA_MENU_STAGE_FILE="$PREVIEW/stage"
printf 'ready\n' > "$QA_MENU_STAGE_FILE"
export QA_MENU_PREVIEW_SECONDS=28800 QA_MENU_STDIN=1
unset QA_MENU_AUTOMATED
printf 'Starting sealed preview; no host Ollama, models or services are used.\n'
exec "$PREVIEW/menu-preview.test" -test.v -test.run '^TestMenuNativePreview$' -test.timeout 8h5m
