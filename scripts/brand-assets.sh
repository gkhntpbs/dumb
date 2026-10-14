#!/usr/bin/env bash
# Regenerates the DUMB bear brand assets from a directory of source PNGs.
# Usage: scripts/brand-assets.sh <source-dir>
# Sources: dumb-bear-icon-{16..512}.png, dumb-bear-menubar-template-{16..512}.png,
# dumb-bear-mascot-{sit,sleep}.png. Requires macOS (sips, iconutil) and python3 with Pillow.
set -euo pipefail
CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$(cd "${1:?source directory required}" && pwd)"
BRAND="$CORE/docs/brand"
EMBED="$CORE/internal/branding/assets"
MAC="$CORE/macos/Resources"
mkdir -p "$BRAND" "$EMBED" "$MAC"

for s in 16 32 64 128 256 512; do
  cp "$SRC/dumb-bear-icon-$s.png" "$BRAND/dumb-bear-icon-$s.png"
  cp "$SRC/dumb-bear-menubar-template-$s.png" "$BRAND/dumb-bear-menubar-template-$s.png"
done
for m in sit sleep; do
  sips -Z 640 "$SRC/dumb-bear-mascot-$m.png" --out "$BRAND/dumb-bear-mascot-$m.png" >/dev/null
done
sips -Z 160 "$SRC/dumb-bear-icon-512.png" --out "$BRAND/dumb-bear-icon-160.png" >/dev/null

python3 -I - "$BRAND" "$EMBED" "$MAC" <<'PY'
import io, struct, sys
from PIL import Image
brand, embed, mac = sys.argv[1:4]
def src(kind, size):
    for s in (16, 32, 64, 128, 256, 512):
        if s >= size:
            return Image.open(f"{brand}/dumb-bear-{kind}-{s}.png").convert("RGBA")
def scaled(kind, size):
    im = src(kind, size)
    return im if im.width == size else im.resize((size, size), Image.LANCZOS)
def png(im):
    b = io.BytesIO(); im.save(b, "PNG", optimize=True); return b.getvalue()
def ico(path, sizes):
    blobs = [png(scaled("icon", s)) for s in sizes]
    out = struct.pack("<HHH", 0, 1, len(sizes)); off = 6 + 16 * len(sizes)
    for s, b in zip(sizes, blobs):
        out += struct.pack("<BBBBHHII", s % 256, s % 256, 0, 0, 1, 32, len(b), off); off += len(b)
    open(path, "wb").write(out + b"".join(blobs))
ico(f"{embed}/favicon.ico", [16, 32, 48])
ico(f"{embed}/tray.ico", [16, 20, 24, 32, 48, 64, 256])
scaled("icon", 128).save(f"{embed}/icon-128.png", optimize=True)
scaled("icon", 180).save(f"{embed}/apple-touch-icon.png", optimize=True)
for s, name in ((18, "menubar.png"), (36, "menubar@2x.png")):
    scaled("menubar-template", s).save(f"{mac}/{name}", optimize=True)
PY

ICONSET="$(mktemp -d)/AppIcon.iconset"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  cp "$BRAND/dumb-bear-icon-$s.png" "$ICONSET/icon_${s}x${s}.png"
done
for s in 16 128 256; do
  cp "$BRAND/dumb-bear-icon-$((s * 2)).png" "$ICONSET/icon_${s}x${s}@2x.png"
done
cp "$BRAND/dumb-bear-icon-64.png" "$ICONSET/icon_32x32@2x.png"
iconutil -c icns "$ICONSET" -o "$MAC/AppIcon.icns"
rm -rf "$(dirname "$ICONSET")"
printf 'Brand assets regenerated.\n'
