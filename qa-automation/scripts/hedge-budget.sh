#!/usr/bin/env bash
# Hedge-budget measurement against the real local Ollama. Not part of the gate;
# runs only with QA_OLLAMA=1.
#
# Mimics a caller like Moonlet that waits 1.5 s before taking its cloud path:
# built dumb-server + one dumb-worker on 127.0.0.1, then N sequential
#   POST /v1/jobs?wait=1500ms  {"format":"json", "deadline_ms":<QA_DEADLINE_MS>}
# and reports p50 / p95 / max total latency (curl time_total) and how many came
# back done within 1.5 s. Then the 503 latency: before any worker said hello,
# and right after the worker stopped (goodbye).
#
# Environment: QA_OLLAMA=1 (required), QA_MODEL (default smollm2:135m),
# QA_N (default 20), QA_DEADLINE_MS (default 10000), DUMB_OLLAMA (worker's Ollama URL).
# Usage: QA_OLLAMA=1 ./qa-automation/scripts/hedge-budget.sh
set -euo pipefail
[[ "${QA_OLLAMA:-}" == "1" ]] || { echo "hedge-budget: set QA_OLLAMA=1 (needs a local Ollama with the model)"; exit 3; }

CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MODEL="${QA_MODEL:-smollm2:135m}"; N="${QA_N:-20}"; DEADLINE="${QA_DEADLINE_MS:-10000}"
T="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]:-}"; do [[ -n "$p" ]] && kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$T"
}
trap cleanup EXIT

cd "$CORE"
go build -o "$T/dumb-server" ./cmd/dumb-server
go build -ldflags "-X main.version=0.0.1-qa" -o "$T/dumb-worker" ./cmd/dumb-worker
tok() { head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; }
WT="$(tok)"; CT="$(tok)"
umask 077
printf '%s' "$WT" > "$T/wtoken"
{ echo "qa-worker worker $(printf '%s' "$WT" | shasum -a 256 | cut -d' ' -f1)"
  echo "qa-caller caller $(printf '%s' "$CT" | shasum -a 256 | cut -d' ' -f1)"; } > "$T/tokens"
PORT=$((20000 + RANDOM % 20000)); BASE="http://127.0.0.1:$PORT"
DUMB_ADDR="127.0.0.1:$PORT" DUMB_TOKENS_FILE="$T/tokens" "$T/dumb-server" > "$T/server.log" 2>&1 &
PIDS+=($!)
for _ in $(seq 50); do curl -s -o /dev/null "$BASE/" && break; sleep 0.1; done

post() { # body → "code time_total" (body in $T/out)
  curl -s -o "$T/out" -w '%{http_code} %{time_total}\n' -H "Authorization: Bearer $CT" \
    -H 'Content-Type: application/json' "$BASE/v1/jobs?wait=1500ms" -d "$1"
}
body() { printf '{"model":"%s","prompt":"Return a JSON object with the field n set to %s and the field word set to a random English word.","format":"json","deadline_ms":%s}' "$MODEL" "$1" "$DEADLINE"; }

echo "═══ 503 with no worker ever online ═══"
for i in 1 2 3 4 5; do post "$(body 0)"; done | tee "$T/503a"

"$T/dumb-worker" run -server "$BASE" -token-file "$T/wtoken" -models "$MODEL" > "$T/worker.log" 2>&1 &
WPID=$!; PIDS+=($WPID)
for _ in $(seq 100); do grep -q "hello: worker" "$T/worker.log" && break; sleep 0.1; done
grep -q "hello: worker" "$T/worker.log" || { cat "$T/worker.log"; echo "worker never said hello"; exit 1; }

echo "═══ warm-up (model load, not counted) ═══"
post "$(body warm)"; cat "$T/out"; echo

echo "═══ $N requests, wait=1500ms, format=json ═══"
: > "$T/lat"
for i in $(seq "$N"); do
  r="$(post "$(body "$i")")"
  state="$(sed -n 's/.*"state":"\([a-z]*\)".*/\1/p' "$T/out")"
  echo "$r $state" | tee -a "$T/lat"
done

echo "═══ 503 right after the worker stopped (goodbye) ═══"
kill -TERM "$WPID"; wait "$WPID" 2>/dev/null || true
grep -E "stopped|goodbye" "$T/worker.log" || true
for i in 1 2 3 4 5; do post "$(body 0)"; done | tee "$T/503b"

echo "═══ worker job times ═══"
grep -E "job .*: (done|failed)|result refused|revoked" "$T/worker.log" | sed 's/^dumb-worker: [0-9/]* //' || true

echo "═══ summary ═══"
python3 -I - "$T/lat" "$T/503a" "$T/503b" <<'PY'
import sys
def rows(p): return [l.split() for l in open(p) if l.strip()]
lat = rows(sys.argv[1])
ms = sorted(float(r[1]) * 1000 for r in lat)
def pct(p): return ms[min(len(ms) - 1, int(round(p / 100 * (len(ms) - 1))))]
within = sum(1 for r in lat if r[0] == "200" and len(r) > 2 and r[2] == "done" and float(r[1]) <= 1.5)
print(f"requests: {len(ms)}  p50 {pct(50):.0f} ms  p95 {pct(95):.0f} ms  max {ms[-1]:.0f} ms")
print(f"done within 1.5 s: {within}/{len(ms)}  status codes: {sorted(set(r[0] for r in lat))}")
for name, p in (("503, no worker ever", sys.argv[2]), ("503, after goodbye", sys.argv[3])):
    r = rows(p)
    v = [float(x[1]) * 1000 for x in r]
    print(f"{name}: codes {sorted(set(x[0] for x in r))}  median {sorted(v)[len(v)//2]:.1f} ms  max {max(v):.1f} ms")
PY
