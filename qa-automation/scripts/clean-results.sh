#!/usr/bin/env bash
# Removes QA output so result folders do not grow without bound.
# Deletes (all gitignored, regenerated on every run):
#   - EVERY run folder under **/results/ (.gitkeep is KEPT)
#   - stray output: **/undefined/, .maestro/, test-results/, playwright-report/
# Usage: ./clean-results.sh [--dry-run]
set -euo pipefail

QA_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY=0; [[ "${1:-}" == "--dry-run" || "${1:-}" == "-n" ]] && DRY=1

targets=()
while IFS= read -r -d '' rdir; do
  while IFS= read -r -d '' entry; do targets+=("$entry"); done \
    < <(find "$rdir" -mindepth 1 -maxdepth 1 ! -name .gitkeep -print0)
done < <(find "$QA_DIR" -maxdepth 3 -type d -name results -not -path '*/node_modules/*' -print0)
while IFS= read -r -d '' stray; do targets+=("$stray"); done \
  < <(find "$QA_DIR" -maxdepth 3 -type d \( -name undefined -o -name .maestro -o -name test-results -o -name playwright-report \) -not -path '*/node_modules/*' -print0)

if [[ ${#targets[@]} -eq 0 ]]; then
  echo "Clean — no test output to remove."
  exit 0
fi

TOTAL="$(du -sch "${targets[@]}" 2>/dev/null | tail -1 | cut -f1)"
echo "Removing ${#targets[@]} item(s) (total ${TOTAL:-?}):"
for t in "${targets[@]}"; do echo "  - ${t#"$QA_DIR"/}"; done

if [[ $DRY -eq 1 ]]; then
  echo "(--dry-run — nothing was removed)"
  exit 0
fi
rm -rf "${targets[@]}"
echo "✅ Cleaned."
