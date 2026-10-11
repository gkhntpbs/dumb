#!/usr/bin/env bash
# macOS launchd end to end, on the real machine, reversibly. Not part of the
# gate; runs only with QA_LAUNCHD=1 because it installs (and removes) the user
# agent ~/Library/LaunchAgents/dumb-worker.plist. Needs a local Ollama with
# QA_MODEL (default smollm2:135m).
#
#   1. Builds dumb-server, fakegh and dumb-worker 0.1.0 / 0.2.0 (throwaway
#      minisign key) into a temp dir; dumb-server runs on 127.0.0.1.
#   2. `dumb-worker service install` (0.1.0) → launchctl print shows it running →
#      a caller job is served by the launchd-run worker.
#   3. kill -9 → launchd starts a new process → it serves again.
#   4. dumb-server restarts with minimum version 0.2.0 → 426 → the worker
#      installs 0.2.0 from fakegh → exit 4 → launchd restarts it → 0.2.0 serves.
#      The updated binary must carry no com.apple.quarantine xattr.
#   5. `dumb-worker service uninstall` → launchctl no longer knows it, the plist
#      is gone, and the worker said goodbye (status shows nobody online).
# The temp dir (binaries, token, logs) is removed at the end; the trap
# uninstalls the agent if a step fails.
# Usage: QA_LAUNCHD=1 ./qa-automation/scripts/launchd-e2e.sh
set -euo pipefail
[[ "$(uname)" == "Darwin" ]] || { echo "launchd-e2e: macOS only"; exit 3; }
[[ "${QA_LAUNCHD:-}" == "1" ]] || { echo "launchd-e2e: set QA_LAUNCHD=1 (installs a user LaunchAgent, removed at the end)"; exit 3; }

CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MODEL="${QA_MODEL:-smollm2:135m}"
LABEL="gui/$(id -u)/dumb-worker"
PLIST="$HOME/Library/LaunchAgents/dumb-worker.plist"
[[ ! -e "$PLIST" ]] || { echo "launchd-e2e: $PLIST already exists; refusing to touch it"; exit 1; }
T="$(mktemp -d)"; T="$(cd "$T" && pwd -P)"
PIDS=()
cleanup() {
  [[ -x "$T/install/dumb-worker" ]] && "$T/install/dumb-worker" service uninstall >/dev/null 2>&1 || true
  launchctl bootout "$LABEL" 2>/dev/null || true
  rm -f "$PLIST"
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  pkill -f "$T/" 2>/dev/null || true
  rm -rf "$T"
}
trap cleanup EXIT
fail() { echo "❌ FAIL: $*"; tail -30 "$T/logs/dumb-worker.log" 2>/dev/null || true; exit 1; }
wait_for() { # pattern file seconds
  for _ in $(seq $(($3 * 10))); do grep -q "$1" "$2" 2>/dev/null && return 0; sleep 0.1; done
  return 1
}
lpid() { launchctl print "$LABEL" 2>/dev/null | awk '/^\tpid = /{print $3}'; }

GOOS="$(go env GOOS)"; GOARCH="$(go env GOARCH)"
mkdir -p "$T/bin" "$T/releases/v0.2.0" "$T/install" "$T/build/0.1.0" "$T/build/0.2.0" "$T/logs"
cd "$CORE"
echo "═══ build ═══"
go build -o "$T/bin/fakegh" ./internal/update/e2e/fakegh
go build -o "$T/bin/dumb-server" ./cmd/dumb-server
"$T/bin/fakegh" keygen -dir "$T/key" >/dev/null
PUB="$(cat "$T/key/key.pub")"
for v in 0.1.0 0.2.0; do
  CGO_ENABLED=0 go build -trimpath -o "$T/build/$v/dumb-worker" \
    -ldflags "-X main.version=$v -X github.com/gkhntpbs/dumb/internal/update.PublicKey=$PUB" ./cmd/dumb-worker
done
ARC="dumb-worker_0.2.0_${GOOS}_${GOARCH}.tar.gz"
tar -C "$T/build/0.2.0" -czf "$T/releases/v0.2.0/$ARC" dumb-worker
(cd "$T/releases/v0.2.0" && shasum -a 256 "$ARC" > checksums.txt)
"$T/bin/fakegh" sign -key "$T/key/key.secret" "$T/releases/v0.2.0/checksums.txt" >/dev/null
# The first two release listings (startup checks of the first process and of
# the one launchd starts after kill -9) fail, so only the 426 path updates.
"$T/bin/fakegh" serve -root "$T/releases" -addr-file "$T/gh.url" -fail-first 2 > "$T/fakegh.log" 2>&1 &
PIDS+=($!)

tok() { head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
WT="$(tok)"; CT="$(tok)"
(umask 077; printf '%s' "$WT" > "$T/token")
{ echo "w1 worker $(printf '%s' "$WT" | shasum -a 256 | cut -d' ' -f1)"
  echo "app caller $(printf '%s' "$CT" | shasum -a 256 | cut -d' ' -f1)"; } > "$T/tokens"
PORT=$((20000 + RANDOM % 20000)); BASE="http://127.0.0.1:$PORT"
start_server() { # min version
  DUMB_ADDR="127.0.0.1:$PORT" DUMB_TOKENS_FILE="$T/tokens" DUMB_MIN_WORKER_VERSION="$1" \
    "$T/bin/dumb-server" >> "$T/server.log" 2>&1 &
  SERVER=$!; PIDS+=($SERVER)
  for _ in $(seq 50); do curl -s -o /dev/null "$BASE/" && return 0; sleep 0.1; done
  fail "server did not start"
}
start_server 0.0.0
for _ in $(seq 50); do [[ -s "$T/gh.url" ]] && break; sleep 0.1; done
GH="$(cat "$T/gh.url")"

job() { # → prints the job status; fails unless done
  local out
  out="$(curl -s -H "Authorization: Bearer $CT" -H 'Content-Type: application/json' "$BASE/v1/jobs?wait=10s" \
    -d "{\"model\":\"$MODEL\",\"prompt\":\"Return {\\\"ok\\\":true}\",\"format\":\"json\",\"deadline_ms\":15000}")"
  echo "$out"
  [[ "$out" == *'"state":"done"'* ]] || fail "job not done: $out"
  local id="${out#*\"job_id\":\"}"; id="${id%%\"*}"
  wait_for "job $id: done" "$T/logs/dumb-worker.log" 5 || fail "job $id not in the launchd worker's log"
  echo "served by the launchd worker: $(grep "job $id: done" "$T/logs/dumb-worker.log")"
}
online() { # wait until the server sees a worker
  for _ in $(seq 300); do
    curl -s -H "Authorization: Bearer $CT" "$BASE/v1/status" | grep -q '"workers_online":1' && return 0; sleep 0.1
  done
  fail "worker never came online"
}

echo "═══ 1. service install (0.1.0) ═══"
cp "$T/build/0.1.0/dumb-worker" "$T/install/dumb-worker"
"$T/install/dumb-worker" service install -server "$BASE" -token-file "$T/token" -models "$MODEL" \
  -update-url "$GH" -log-dir "$T/logs"
[[ -f "$PLIST" ]] || fail "no plist"
plutil -lint "$PLIST"
online
launchctl print "$LABEL" | grep -E "^\s+(state|pid|program|last exit code|runs) = " || fail "launchctl print"
P1="$(lpid)"; [[ -n "$P1" ]] || fail "no pid"
job

echo "═══ 2. kill -9 $P1 → launchd restarts ═══"
kill -9 "$P1"
for _ in $(seq 300); do P2="$(lpid)"; [[ -n "$P2" && "$P2" != "$P1" ]] && break; sleep 0.1; done
[[ -n "$P2" && "$P2" != "$P1" ]] || fail "launchd did not restart the worker"
launchctl print "$LABEL" | grep -E "^\s+(state|pid|last terminating signal|runs) = "
online
job

echo "═══ 3. 426 → self-update → exit 4 → launchd restarts 0.2.0 ═══"
kill "$SERVER"; wait "$SERVER" 2>/dev/null || true
start_server 0.2.0
wait_for "update: 0.2.0 committed" "$T/logs/dumb-worker.log" 90 || fail "no update commit"
P3="$(lpid)"
grep -E "UPGRADE REQUIRED|installed 0.2.0|version 0.2.0|committed" "$T/logs/dumb-worker.log"
launchctl print "$LABEL" | grep -E "^\s+(state|pid|last exit code|runs) = "
launchctl print "$LABEL" | grep -q "last exit code = 4" || fail "launchd did not see exit code 4"
[[ "$P3" != "$P2" ]] || fail "same pid after the update"
"$T/install/dumb-worker" version | grep -q "dumb-worker 0.2.0" || fail "installed binary is not 0.2.0"
online
job
echo "─── Gatekeeper ───"
echo "xattr -l (empty = no quarantine):"; xattr -l "$T/install/dumb-worker"
xattr -l "$T/install/dumb-worker" | grep -q com.apple.quarantine && fail "quarantine xattr on the updated binary"
codesign -dv "$T/install/dumb-worker" 2>&1 | grep -E "Signature|flags" || true
spctl --assess --type execute -vv "$T/install/dumb-worker" 2>&1 || true
echo "launchd runs it: pid $P3 is $(ps -o comm= -p "$P3")"

echo "═══ 4. service uninstall ═══"
"$T/install/dumb-worker" service uninstall
launchctl print "$LABEL" >/dev/null 2>&1 && fail "launchctl still knows $LABEL"
echo "launchctl print: $(launchctl print "$LABEL" 2>&1 | tail -1)"
[[ ! -e "$PLIST" ]] || fail "plist still there"
echo "plist gone: $PLIST"
sleep 0.5
kill -0 "$P3" 2>/dev/null && fail "worker $P3 still running"
grep -E "stopped" "$T/logs/dumb-worker.log" | tail -1
curl -s -H "Authorization: Bearer $CT" "$BASE/v1/status"; echo
grep -q '"msg":"worker goodbye"' "$T/server.log" || fail "no goodbye on uninstall"
echo "✅ launchd e2e PASSED"
