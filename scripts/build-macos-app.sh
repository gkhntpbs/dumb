#!/usr/bin/env bash
set -euo pipefail
CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${1:-$CORE/dist/DUMB Worker.app}"
VERSION="${DUMB_BUILD_VERSION:-0.0.0}"
MARKER='DUMB local macOS build v1'
if [[ "$OUTPUT" != /* ]]; then
  OUTPUT="$PWD/$OUTPUT"
fi
if [[ "$(uname -s)" != Darwin ]]; then
  printf 'The macOS application must be built on macOS.\n' >&2
  exit 1
fi
if [[ $# -gt 1 || ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-beta\.[0-9]+)?$ ]]; then
  printf 'Supply at most one output path and a valid DUMB_BUILD_VERSION.\n' >&2
  exit 1
fi
if [[ -e "$OUTPUT" || -L "$OUTPUT" ]]; then
  if [[ -L "$OUTPUT" || ! -d "$OUTPUT" || -L "$OUTPUT/Contents/.dumb-local-build" || ! -f "$OUTPUT/Contents/.dumb-local-build" || "$(cat "$OUTPUT/Contents/.dumb-local-build")" != "$MARKER" ]]; then
    printf 'Refusing to replace an application without the expected local build marker.\n' >&2
    exit 1
  fi
fi
command -v go >/dev/null
command -v swiftc >/dev/null
PARENT="$(dirname "$OUTPUT")"
mkdir -p "$PARENT"
LOCK="$OUTPUT.dumb-build-lock"
if ! mkdir "$LOCK"; then
  printf 'Another build holds the output lock: %s\n' "$LOCK" >&2
  exit 1
fi
STAGE=''
BACKUP=''
cleanup() {
  if [[ -n "$BACKUP" && -d "$BACKUP/application" && ! -e "$OUTPUT" && ! -L "$OUTPUT" ]]; then
    mv "$BACKUP/application" "$OUTPUT" || printf 'Previous local build remains at %s/application\n' "$BACKUP" >&2
  fi
  if [[ -n "$STAGE" ]]; then
    rm -rf "$STAGE"
  fi
  if [[ -n "$BACKUP" && ! -e "$BACKUP/application" ]]; then
    rmdir "$BACKUP"
  fi
  rmdir "$LOCK"
}
trap cleanup EXIT
STAGE="$(mktemp -d "$PARENT/.dumb-macos-build.XXXXXX")"
mkdir -p "$STAGE/Contents/MacOS" "$STAGE/Contents/Resources/DUMBMenu.app/Contents/MacOS"
cp "$CORE/macos/Info.plist" "$STAGE/Contents/Info.plist"
cp "$CORE/macos/MenuInfo.plist" "$STAGE/Contents/Resources/DUMBMenu.app/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString ${VERSION%%-*}" "$STAGE/Contents/Info.plist"
swiftc -swift-version 5 -target "$(uname -m)-apple-macosx13.0" -O -framework AppKit "$CORE/macos/MenuHelper.swift" -o "$STAGE/Contents/Resources/DUMBMenu.app/Contents/MacOS/dumb-menu"
cd "$CORE"
CGO_ENABLED=0 GOMAXPROCS=2 GOPROXY=off GOSUMDB=off go build -p 1 -trimpath -ldflags "-X main.version=$VERSION" -o "$STAGE/Contents/MacOS/dumb-worker" ./cmd/dumb-worker
printf '%s\n' "$MARKER" > "$STAGE/Contents/.dumb-local-build"
if [[ -e "$OUTPUT" || -L "$OUTPUT" ]]; then
  if [[ -L "$OUTPUT" || ! -d "$OUTPUT" || -L "$OUTPUT/Contents/.dumb-local-build" || ! -f "$OUTPUT/Contents/.dumb-local-build" || "$(cat "$OUTPUT/Contents/.dumb-local-build")" != "$MARKER" ]]; then
    printf 'Output changed during compilation; refusing to replace it.\n' >&2
    exit 1
  fi
  BACKUP="$(mktemp -d "$PARENT/.dumb-macos-backup.XXXXXX")"
  mv "$OUTPUT" "$BACKUP/application"
fi
mv "$STAGE" "$OUTPUT"
STAGE=''
if [[ -n "$BACKUP" ]]; then
  rm -rf "$BACKUP/application"
  rmdir "$BACKUP"
  BACKUP=''
fi
printf 'Built local application: %s\n' "$OUTPUT"
