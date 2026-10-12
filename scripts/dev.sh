#!/usr/bin/env bash
# Local development run: dumb-server + one dumb-worker against the local Ollama.
#
# Builds both binaries into a temp dir, generates throwaway worker and caller
# tokens there, starts dumb-server on 127.0.0.1 and one worker, prints the caller
# token and an example curl, and follows both logs (worker version 0.0.1-dev,
# above the server's default minimum 0.0.0). Ctrl-C stops both processes
# and deletes the temp dir. Self-update is off (no release key is embedded).
#
# Environment (all optional):
#   DEV_PORT     server port (default 8080)
#   DEV_MODELS   models the worker serves (default smollm2:135m; must be in Ollama)
#   DUMB_OLLAMA  Ollama URL (default http://127.0.0.1:11434)
# Usage: ./scripts/dev.sh
set -euo pipefail

CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PORT="${DEV_PORT:-8080}"
MODELS="${DEV_MODELS:-smollm2:135m}"
T="$(mktemp -d "${TMPDIR:-/tmp}/dumb-dev.XXXXXX")"
PIDS=()
cleanup() {
  trap - EXIT INT TERM
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$T"
  echo "dev: stopped, $T removed"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

if curl -s -o /dev/null "http://127.0.0.1:$PORT/"; then
  echo "dev: something already listens on 127.0.0.1:$PORT (set DEV_PORT)" >&2; exit 1
fi

cd "$CORE"
echo "dev: building into $T/bin"
go build -o "$T/bin/dumb-server" ./cmd/dumb-server
go build -ldflags "-X main.version=0.0.1-dev" -o "$T/bin/dumb-worker" ./cmd/dumb-worker

token() { head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
hash() { printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1; }
WTOKEN="$(token)"; CTOKEN="$(token)"
umask 077
printf '%s' "$WTOKEN" > "$T/worker.token"
{ echo "dev-worker worker $(hash "$WTOKEN")"; echo "dev-caller caller $(hash "$CTOKEN")"; } > "$T/tokens"

DUMB_ADDR="127.0.0.1:$PORT" DUMB_TOKENS_FILE="$T/tokens" "$T/bin/dumb-server" > "$T/server.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 50); do curl -s -o /dev/null "http://127.0.0.1:$PORT/" && break; sleep 0.1; done
"$T/bin/dumb-worker" run -headless -server "http://127.0.0.1:$PORT" -token-file "$T/worker.token" -models "$MODELS" > "$T/worker.log" 2>&1 &
PIDS+=($!)

FIRST="${MODELS%%,*}"
cat <<MSG

dev: dumb-server on http://127.0.0.1:$PORT, one worker serving $MODELS
dev: caller token: $CTOKEN

  curl -s -H "Authorization: Bearer $CTOKEN" http://127.0.0.1:$PORT/v1/status
  curl -s -H "Authorization: Bearer $CTOKEN" -H 'Content-Type: application/json' \\
    "http://127.0.0.1:$PORT/v1/jobs?wait=5s" \\
    -d '{"model":"$FIRST","prompt":"Reply with {\"ok\":true}","format":"json","deadline_ms":10000}'

dev: logs follow (server JSON, worker text). Ctrl-C stops everything.

MSG
tail -n +1 -f "$T/server.log" "$T/worker.log" &
PIDS+=($!)
# Exit (and clean up) as soon as either process dies.
while kill -0 "${PIDS[0]}" 2>/dev/null && kill -0 "${PIDS[1]}" 2>/dev/null; do sleep 1; done
echo "dev: a process exited; see the logs above" >&2
exit 1
