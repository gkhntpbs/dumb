#!/usr/bin/env bash
# Local end-to-end test of the signed self-update, without GitHub. Not part of
# the gate (it builds binaries and runs processes); run it when the updater changes.
#
#   1. A throwaway minisign key is generated (fakegh keygen) in a temp dir.
#   2. dumb-worker 0.1.0 and 0.2.0 are built with that public key embedded.
#   3. fakegh serves a v0.2.0 release (archive, checksums.txt, .minisig) on 127.0.0.1;
#      its first release listing fails so the startup check misses and the 426
#      path does the update.
#   4. dumb-server runs on 127.0.0.1 with minimum worker version 0.2.0.
#   5. A loop stands in for the service manager: it restarts the worker on exit 4.
#   Pass:  0.1.0 → 426 → installs 0.2.0 → exit 4 → 0.2.0 says hello → commit.
#   Then the archive is tampered with: the worker must refuse it, keep 0.1.0
#   byte-identical, and stay up (no exit).
# Everything lives in a temp dir that is removed at the end, with all processes.
# Usage: ./update-e2e.sh (no arguments). Exit 0 = pass.
set -euo pipefail

CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
T="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  pkill -f "$T/" 2>/dev/null || true
  rm -rf "$T"
}
trap cleanup EXIT
fail() { echo "❌ FAIL: $*"; exit 1; }

GOOS="$(go env GOOS)"; GOARCH="$(go env GOARCH)"
mkdir -p "$T/bin" "$T/releases/v0.2.0" "$T/install" "$T/build/0.1.0" "$T/build/0.2.0"
cd "$CORE"

echo "═══ build helpers and worker versions ═══"
go build -o "$T/bin/fakegh" ./internal/update/e2e/fakegh
go build -o "$T/bin/dumb-server" ./cmd/dumb-server
"$T/bin/fakegh" keygen -dir "$T/key"
PUB="$(cat "$T/key/key.pub")"
echo "throwaway public key: $PUB"
for v in 0.1.0 0.2.0; do
  CGO_ENABLED=0 go build -trimpath -o "$T/build/$v/dumb-worker" \
    -ldflags "-X main.version=$v -X github.com/gkhntpbs/dumb/internal/update.PublicKey=$PUB" ./cmd/dumb-worker
  "$T/build/$v/dumb-worker" version
done

publish() { # (re)create the v0.2.0 release; $1 = "tamper" to corrupt the archive after signing
  local rel="$T/releases/v0.2.0" arc="dumb-worker_0.2.0_${GOOS}_${GOARCH}.tar.gz"
  rm -f "$rel"/*
  tar -C "$T/build/0.2.0" -czf "$rel/$arc" dumb-worker
  (cd "$rel" && shasum -a 256 "$arc" > checksums.txt)
  "$T/bin/fakegh" sign -key "$T/key/key.secret" "$rel/checksums.txt"
  if [[ "${1:-}" == "tamper" ]]; then printf 'tampered' >> "$rel/$arc"; fi
  ls "$rel"
}

echo "═══ start fake GitHub and dumb-server ═══"
publish
"$T/bin/fakegh" serve -root "$T/releases" -addr-file "$T/gh.url" -fail-first 1 > "$T/fakegh.log" 2>&1 &
PIDS+=($!)
TOKEN="e2e-worker-token-$RANDOM$RANDOM"
printf '%s' "$TOKEN" > "$T/token"; chmod 600 "$T/token"
echo "w1 worker $(printf '%s' "$TOKEN" | shasum -a 256 | cut -d' ' -f1)" > "$T/tokens"
PORT=$((20000 + RANDOM % 20000))
DUMB_ADDR="127.0.0.1:$PORT" DUMB_TOKENS_FILE="$T/tokens" DUMB_MIN_WORKER_VERSION=0.2.0 \
  "$T/bin/dumb-server" > "$T/server.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 50); do [[ -s "$T/gh.url" ]] && curl -s "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break; sleep 0.1; done
GH="$(cat "$T/gh.url")"

# The service manager stand-in: restart on exit 4, stop on anything else.
cat > "$T/bin/svc.sh" <<EOF
#!/usr/bin/env bash
for i in 1 2 3 4 5; do
  "$T/install/dumb-worker" run -server http://127.0.0.1:$PORT -token-file "$T/token" -models e2e:model -update-url "$GH"
  rc=\$?
  echo "svc: worker exited with \$rc"
  [[ \$rc -eq 4 ]] || exit \$rc
done
EOF
chmod +x "$T/bin/svc.sh"

wait_for() { # pattern file seconds
  for _ in $(seq $(($3 * 10))); do grep -q "$1" "$2" && return 0; sleep 0.1; done
  return 1
}

echo "═══ run 1: 0.1.0 must update itself to 0.2.0 ═══"
cp "$T/build/0.1.0/dumb-worker" "$T/install/dumb-worker"
"$T/bin/svc.sh" > "$T/run1.log" 2>&1 &
SVC=$!; PIDS+=($SVC)
wait_for "update: 0.2.0 committed" "$T/run1.log" 30 || { cat "$T/run1.log"; fail "no commit"; }
kill "$SVC" 2>/dev/null || true; pkill -f "$T/install/dumb-worker" || true; sleep 0.5
cat "$T/run1.log"
"$T/install/dumb-worker" version | grep -q "dumb-worker 0.2.0" || fail "installed binary is not 0.2.0"
cmp -s "$T/install/dumb-worker" "$T/build/0.2.0/dumb-worker" || fail "installed binary differs from the 0.2.0 build"
for f in .prev .new .update.json; do [[ ! -e "$T/install/dumb-worker$f" ]] || fail "dumb-worker$f left behind"; done
grep -q "svc: worker exited with 4" "$T/run1.log" || fail "no exit 4"
echo "✅ run 1: $("$T/install/dumb-worker" version) installed, committed after hello, no leftovers"

echo "═══ run 2: a tampered archive must be refused ═══"
publish tamper
rm -f "$T/install/"*
cp "$T/build/0.1.0/dumb-worker" "$T/install/dumb-worker"
"$T/bin/svc.sh" > "$T/run2.log" 2>&1 &
SVC=$!; PIDS+=($SVC)
wait_for "does not match the signed checksum" "$T/run2.log" 30 || { cat "$T/run2.log"; fail "tampered archive not refused"; }
wait_for "staying up" "$T/run2.log" 30 || { cat "$T/run2.log"; fail "worker did not stay up after 426"; }
sleep 2
kill -0 "$SVC" 2>/dev/null || { cat "$T/run2.log"; fail "worker exited"; }
kill "$SVC" 2>/dev/null || true; pkill -f "$T/install/dumb-worker" || true; sleep 0.5
cat "$T/run2.log"
cmp -s "$T/install/dumb-worker" "$T/build/0.1.0/dumb-worker" || fail "0.1.0 binary was modified"
grep -q "svc: worker exited" "$T/run2.log" && fail "worker exited during run 2"
for f in .prev .new .update.json; do [[ ! -e "$T/install/dumb-worker$f" ]] || fail "dumb-worker$f left behind"; done
echo "✅ run 2: tampered archive refused, 0.1.0 byte-identical, worker stayed up"
echo "═══ fake GitHub log ═══"; cat "$T/fakegh.log"
echo "✅ update e2e PASSED"
