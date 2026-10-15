#!/bin/sh
# install.sh — download, verify and install dumb-worker on Linux (amd64, arm64).
#
# DUMB = Distributed User-owned Model Backend. This script downloads the release
# archive for this machine from GitHub Releases, verifies checksums.txt against
# the minisign signature with the public key below (OpenSSL 3 does the Ed25519
# and BLAKE2b work; minisign itself is not needed), checks the archive's
# SHA-256 against the signed checksums, and only then extracts the binary and
# runs `dumb-worker install` with the remaining arguments.
#
# Usage (read it first; do not pipe it into a shell when pairing from stdin):
#
#   sh install.sh [--version X.Y.Z] [--download-only DIR] [dumb-worker install flags]
#
#   sh install.sh -user -server https://dumb.example -models qwen3:0.6b -pair-code-stdin
#   sudo sh install.sh -system -server https://dumb.example -models qwen3:0.6b -pair-code-file /root/code
#
# --version        install this release (default: the latest stable release)
# --download-only  verify and place the binary in DIR, then stop (no install)
#
# Requirements: curl or wget, tar, gzip, od, dd, awk and openssl >= 3.0.
# The environment variables DUMB_INSTALL_API, DUMB_INSTALL_DOWNLOAD and
# DUMB_INSTALL_PUBKEY exist for tests against a local mirror.
set -eu

REPO="gkhntpbs/dumb"
# Release signing key (public). Its secret half never leaves the release owner.
PUBKEY="${DUMB_INSTALL_PUBKEY:-RWTc4l7C1hVicuqNQesONQ4tP3Kw1J8eTJ3dwHaN0nfuYJimkIfXncds}"
API="${DUMB_INSTALL_API:-https://api.github.com}"
DOWNLOAD="${DUMB_INSTALL_DOWNLOAD:-https://github.com/$REPO/releases/download}"

die() { echo "install.sh: $*" >&2; exit 1; }

version=""
download_only=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die "--version needs a value"; version="$2"; shift 2 ;;
    --version=*) version="${1#--version=}"; shift ;;
    --download-only) [ $# -ge 2 ] || die "--download-only needs a directory"; download_only="$2"; shift 2 ;;
    --download-only=*) download_only="${1#--download-only=}"; shift ;;
    --) shift; break ;;
    *) break ;;
  esac
done

[ "$(uname -s)" = "Linux" ] || die "this installer is for Linux; see the README for macOS and Windows"
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported CPU architecture $(uname -m) (amd64 and arm64 are released)" ;;
esac

for tool in tar gzip od dd awk openssl; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
case "$(openssl version 2>/dev/null)" in
  "OpenSSL "[3-9].*) ;;
  *) die "OpenSSL 3.0 or newer is required to verify the release signature (found: $(openssl version 2>/dev/null || echo none))" ;;
esac
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --proto '=https,http' --retry 2 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "curl or wget is required"
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/dumb-install.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM

if [ -z "$version" ]; then
  fetch "$API/repos/$REPO/releases/latest" "$tmp/latest.json" || die "cannot query the latest release"
  version="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)"
fi
version="${version#v}"
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' || die "invalid release version '$version'"

archive="dumb-worker_${version}_linux_${arch}.tar.gz"
echo "dumb-worker $version for linux/$arch"
for f in checksums.txt checksums.txt.minisig "$archive"; do
  fetch "$DOWNLOAD/v$version/$f" "$tmp/$f" || die "cannot download $f"
done

# --- minisign verification (prehashed Ed25519 over BLAKE2b-512) --------------
hex() { od -An -v -tx1 | tr -d ' \n'; }
b64d() { printf '%s' "$1" | openssl base64 -d -A; }
slice() { dd if="$1" bs=1 skip="$2" count="$3" 2>/dev/null; }

b64d "$PUBKEY" > "$tmp/pk.bin"
[ "$(wc -c < "$tmp/pk.bin" | tr -d ' ')" = 42 ] || die "malformed public key"
[ "$(slice "$tmp/pk.bin" 0 2 | hex)" = "4564" ] || die "public key is not Ed25519"   # "Ed"
pk_id="$(slice "$tmp/pk.bin" 2 8 | hex)"
# DER SubjectPublicKeyInfo for Ed25519: fixed 12-byte prefix + 32-byte key.
{ printf '\060\052\060\005\006\003\053\145\160\003\041\000'; slice "$tmp/pk.bin" 10 32; } > "$tmp/pk.der"

sig_line="$(sed -n 2p "$tmp/checksums.txt.minisig")"
tc_line="$(sed -n 3p "$tmp/checksums.txt.minisig")"
gsig_line="$(sed -n 4p "$tmp/checksums.txt.minisig")"
b64d "$sig_line" > "$tmp/sig.bin"
[ "$(wc -c < "$tmp/sig.bin" | tr -d ' ')" = 74 ] || die "malformed signature"
[ "$(slice "$tmp/sig.bin" 0 2 | hex)" = "4544" ] || die "signature is not prehashed (ED); refusing"
[ "$(slice "$tmp/sig.bin" 2 8 | hex)" = "$pk_id" ] || die "signature was made with another key"
slice "$tmp/sig.bin" 10 64 > "$tmp/sig.raw"

verify() { # message-file signature-file
  openssl pkeyutl -verify -pubin -keyform DER -inkey "$tmp/pk.der" -rawin -in "$1" -sigfile "$2" >/dev/null 2>&1
}
openssl dgst -blake2b512 -binary "$tmp/checksums.txt" > "$tmp/checksums.b2"
verify "$tmp/checksums.b2" "$tmp/sig.raw" || die "checksums.txt signature does not verify; refusing"
case "$tc_line" in
  "trusted comment: "*) ;;
  *) die "malformed signature (trusted comment)" ;;
esac
{ cat "$tmp/sig.raw"; printf '%s' "${tc_line#trusted comment: }"; } > "$tmp/global.msg"
b64d "$gsig_line" > "$tmp/global.sig"
verify "$tmp/global.msg" "$tmp/global.sig" || die "trusted comment signature does not verify; refusing"
echo "signature: ok (${tc_line#trusted comment: })"

# --- archive checksum, then extraction --------------------------------------
want="$(awk -v n="$archive" '$2 == n || $2 == "*" n { print $1; exit }' "$tmp/checksums.txt")"
[ -n "$want" ] || die "checksums.txt has no entry for $archive"
got="$(openssl dgst -sha256 -r "$tmp/$archive" | awk '{ print $1 }')"
[ "$got" = "$want" ] || die "$archive: SHA-256 $got does not match the signed $want"
echo "archive: ok ($got)"

mkdir "$tmp/x"
tar -xzf "$tmp/$archive" -C "$tmp/x" dumb-worker || die "cannot extract dumb-worker"
[ -f "$tmp/x/dumb-worker" ] && [ ! -L "$tmp/x/dumb-worker" ] || die "the archive holds no regular dumb-worker"
chmod 0755 "$tmp/x/dumb-worker"

if [ -n "$download_only" ]; then
  mkdir -p "$download_only"
  cp "$tmp/x/dumb-worker" "$download_only/dumb-worker.tmp"
  mv "$download_only/dumb-worker.tmp" "$download_only/dumb-worker"
  echo "verified binary: $download_only/dumb-worker"
  exit 0
fi

set +e
"$tmp/x/dumb-worker" install "$@"
rc=$?
set -e
exit $rc
