#!/usr/bin/env bash
# QA çıktılarını temizler — sonuç klasörlerinin şişmesini engeller.
# Siler (hepsi .gitignore'lu, her koşuda yeniden üretilir):
#   - **/results/ altındaki TÜM koşu klasörleri (.gitkeep KORUNUR)
#   - stray çıktılar: **/undefined/, .maestro/, test-results/, playwright-report/
# Kullanım: ./clean-results.sh [--dry-run]
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
  echo "Temiz — silinecek test çıktısı yok."
  exit 0
fi

TOTAL="$(du -sch "${targets[@]}" 2>/dev/null | tail -1 | cut -f1)"
echo "Silinecek ${#targets[@]} öğe (toplam ${TOTAL:-?}):"
for t in "${targets[@]}"; do echo "  - ${t#"$QA_DIR"/}"; done

if [[ $DRY -eq 1 ]]; then
  echo "(--dry-run — hiçbir şey silinmedi)"
  exit 0
fi
rm -rf "${targets[@]}"
echo "✅ Temizlendi."
