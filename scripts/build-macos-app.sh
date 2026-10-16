#!/usr/bin/env bash
set -euo pipefail
CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT="${1:-$CORE/dist/DUMB Worker.app}"
VERSION="${DUMB_BUILD_VERSION:-0.0.0}"
ARCH="${DUMB_BUILD_ARCH:-$(go env GOARCH)}"
PUBLIC_KEY="${DUMB_MINISIGN_PUBLIC_KEY:-}"
ZIP="${DUMB_BUILD_ZIP:-}"
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
owned_output() {
  local marker
  [[ ! -L "$OUTPUT" && -d "$OUTPUT" ]] || return 1
  for marker in "$OUTPUT/Contents/Resources/.dumb-local-build" "$OUTPUT/Contents/.dumb-local-build"; do
    if [[ ! -L "$marker" && -f "$marker" && "$(cat "$marker")" == "$MARKER" ]]; then
      return 0
    fi
  done
  return 1
}
if [[ -e "$OUTPUT" || -L "$OUTPUT" ]]; then
  if ! owned_output; then
    printf 'Refusing to replace an application without the expected local build marker.\n' >&2
    exit 1
  fi
fi
command -v go >/dev/null
command -v swiftc >/dev/null
case "$ARCH" in
  arm64) SWIFT_ARCH=arm64 ;;
  amd64) SWIFT_ARCH=x86_64 ;;
  *) printf 'DUMB_BUILD_ARCH must be arm64 or amd64.\n' >&2; exit 1 ;;
esac
if [[ -n "$PUBLIC_KEY" && ! "$PUBLIC_KEY" =~ ^RW[A-Za-z0-9+/]{54}={0,2}$ ]]; then
  printf 'DUMB_MINISIGN_PUBLIC_KEY must be a minisign public key line.\n' >&2
  exit 1
fi
if [[ -n "$ZIP" && "$ZIP" != /* ]]; then
  ZIP="$PWD/$ZIP"
fi
if [[ -n "$ZIP" && ( -e "$ZIP" || -L "$ZIP" ) ]]; then
  printf 'Refusing to replace an existing archive: %s\n' "$ZIP" >&2
  exit 1
fi
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
mkdir -p "$STAGE/Contents/Resources/DUMBMenu.app/Contents/Resources"
cp "$CORE/macos/Resources/AppIcon.icns" "$STAGE/Contents/Resources/AppIcon.icns"
cp "$CORE/macos/Resources/AppIcon.icns" "$CORE/macos/Resources/menubar.png" "$CORE/macos/Resources/menubar@2x.png" "$STAGE/Contents/Resources/DUMBMenu.app/Contents/Resources/"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString ${VERSION%%-*}" "$STAGE/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion ${VERSION%%-*}" "$STAGE/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion ${VERSION%%-*}" "$STAGE/Contents/Resources/DUMBMenu.app/Contents/Info.plist"
swiftc -module-cache-path "$STAGE/.swift-module-cache" -swift-version 5 -target "$SWIFT_ARCH-apple-macosx13.0" -O -framework AppKit "$CORE/macos/MenuHelper.swift" -o "$STAGE/Contents/Resources/DUMBMenu.app/Contents/MacOS/dumb-menu"
rm -rf "$STAGE/.swift-module-cache"
cd "$CORE"
GOOS=darwin GOARCH="$ARCH" CGO_ENABLED=0 GOMAXPROCS=2 GOPROXY=off GOSUMDB=off go build -p 1 -trimpath -ldflags "-s -w -X main.version=$VERSION -X github.com/gkhntpbs/dumb/internal/update.PublicKey=$PUBLIC_KEY" -o "$STAGE/Contents/MacOS/dumb-worker" ./cmd/dumb-worker
printf '%s\n' "$MARKER" > "$STAGE/Contents/Resources/.dumb-local-build"
codesign --force --sign - "$STAGE/Contents/Resources/DUMBMenu.app"
codesign --force --sign - "$STAGE/Contents/MacOS/dumb-worker"
codesign --force --sign - "$STAGE"
codesign --verify --deep --strict "$STAGE"
plutil -lint "$STAGE/Contents/Info.plist" "$STAGE/Contents/Resources/DUMBMenu.app/Contents/Info.plist"
if [[ -e "$OUTPUT" || -L "$OUTPUT" ]]; then
  if ! owned_output; then
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
if [[ -n "$ZIP" ]]; then
  mkdir -p "$(dirname "$ZIP")"
  ditto -c -k --sequesterRsrc --keepParent "$OUTPUT" "$ZIP"
  printf 'Built application archive: %s\n' "$ZIP"
fi
printf 'Built local application: %s\n' "$OUTPUT"
