#!/usr/bin/env bash
# Scans all reachable git history and the current tracked public tree. Tool
# installation is explicit; reports remain redacted in ignored results/.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
SCANNER="${GITLEAKS_BIN:?set GITLEAKS_BIN to a verified Gitleaks 8.30.0 executable}"
[[ "$("$SCANNER" version)" == "8.30.0" ]] || { echo 'Gitleaks 8.30.0 required' >&2; exit 1; }
OUT="$ROOT/qa-automation/results/public-audit"
mkdir -p "$OUT"
TREE="$(mktemp -d "$OUT/tree.XXXXXX")"
trap 'rm -rf "$TREE"' EXIT
python3 - "$TREE" <<'PY'
import pathlib,subprocess,shutil,sys
root=pathlib.Path.cwd();tree=pathlib.Path(sys.argv[1])
for raw in subprocess.check_output(['git','ls-files','-z']).split(b'\0'):
    if not raw:continue
    source=root/raw.decode();destination=tree/raw.decode()
    if not source.exists() and not source.is_symlink():continue
    destination.parent.mkdir(parents=True,exist_ok=True)
    if source.is_symlink():destination.symlink_to(source.readlink())
    else:shutil.copyfile(source,destination)
PY
"$SCANNER" git --log-opts=--all --redact=100 --no-banner --log-level=warn --report-format=json --report-path="$OUT/history.json" "$ROOT"
"$SCANNER" dir --redact=100 --no-banner --log-level=warn --report-format=json --report-path="$OUT/tree.json" "$TREE"
echo 'Public history and tracked tree audit passed (redacted reports in qa-automation/results/public-audit).'
