#!/usr/bin/env bash
# Derives transparent icon PNG/ICO/ICNS from the sitting mascot and retains
# the supplied menu-bar template images. Requires macOS sips/iconutil and Pillow.
# Usage: uv run --no-project --with pillow==12.0.0 bash scripts/brand-assets.sh <source-dir>
set -euo pipefail
CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$(cd "${1:?source directory required}" && pwd)"
BRAND="$CORE/docs/brand"
EMBED="$CORE/internal/branding/assets"
MAC="$CORE/macos/Resources"
mkdir -p "$BRAND" "$EMBED" "$MAC"

for s in 16 32 64 128 256 512; do
  cp "$SRC/dumb-bear-menubar-template-$s.png" "$BRAND/dumb-bear-menubar-template-$s.png"
done
for m in sit sleep; do
  sips -Z 640 "$SRC/dumb-bear-mascot-$m.png" --out "$BRAND/dumb-bear-mascot-$m.png" >/dev/null
done

python3 -I - "$BRAND" "$EMBED" "$MAC" <<'PY'
import io, struct, sys
from pathlib import Path
from PIL import Image
brand, embed, mac = sys.argv[1:4]
mascot = Image.open(f"{brand}/dumb-bear-mascot-sit.png").convert("RGBA")
for size in (16, 32, 64, 128, 160, 256, 512):
    mascot.resize((size, size), Image.Resampling.LANCZOS).save(f"{brand}/dumb-bear-icon-{size}.png", optimize=True)
def src(kind, size):
    for s in (16, 32, 64, 128, 256, 512):
        if s >= size:
            return Image.open(f"{brand}/dumb-bear-{kind}-{s}.png").convert("RGBA")
def scaled(kind, size):
    if kind == "icon":
        return mascot.resize((size, size), Image.Resampling.LANCZOS)
    im = src(kind, size)
    return im if im.width == size else im.resize((size, size), Image.Resampling.LANCZOS)
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
    target = Path(mac) / name
    image = scaled("menubar-template", s)
    # Keep the existing template byte encoding when its pixels already match.
    if target.exists():
        existing = Image.open(target).convert("RGBA")
        if existing.size == image.size and existing.tobytes() == image.tobytes():
            continue
    image.save(target, optimize=True)
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
