#!/usr/bin/env bash
# KAPI — core. Artefakt üretmeden ÖNCE her şey yeşil OLMALI.
# Sıra: 0) eski çıktıları temizle  1) birim testleri  2) UI testleri (none).
# Herhangi biri kırmızıysa non-zero döner → çağıran (core-build) artefakt ÜRETMEZ.
# Kısmi yeşil ≠ yeşil: cihaz yok / test yok / yapılandırılmadı / env eksik → KIRMIZI.
# Kullanım: ./build-gate.sh (argüman yok)
set -uo pipefail

QA="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGET="${1:-all}"
QA_TOOL="none"
UI_TARGETS=()
export QA_STRICT=1

echo "═══════════ 0. Eski çıktıları temizle ═══════════"
"$QA/scripts/clean-results.sh" || true

echo "═══════════ 1. Birim testleri ═══════════"
"$QA/scripts/unit-tests.sh" "$TARGET"; urc=$?
if [[ $urc -ne 0 ]]; then
  [[ $urc -eq 3 ]] && echo "❌ KAPI KIRMIZI — birim testleri yapılandırılmamış." \
                   || echo "❌ KAPI KIRMIZI — birim testleri geçmedi."
  echo "Artefakt ÜRETİLMEZ."; exit 1
fi

echo "═══════════ 2. UI testleri ($QA_TOOL) ═══════════"
gate=0
if [[ "$QA_TOOL" == "none" ]]; then
  echo "ℹ️  UI testi kapsam dışı (qa=none — bilinçli karar, bkz. docs/adr/0001)."
else
  for t in "${UI_TARGETS[@]}"; do
    if [[ "$TARGET" != "all" && "$t" != "-" && "$t" != "$TARGET" ]]; then continue; fi
    if [[ "$t" == "-" ]]; then "$QA/scripts/run-all-tests.sh"; else "$QA/scripts/run-all-tests.sh" "$t"; fi
    rc=$?
    if [[ $rc -ne 0 ]]; then
      label="$t"; [[ "$t" == "-" ]] && label="$QA_TOOL"
      [[ $rc -eq 3 ]] && echo "❌ $label: UI testleri yapılandırılmamış" || echo "❌ $label: UI testleri KIRMIZI"
      gate=1
    fi
  done
fi

echo "═══════════════════════════════════════════════"
if [[ $gate -eq 0 ]]; then
  echo "✅ KAPI YEŞİL ($TARGET) — artefakt üretimine geçilebilir."
else
  echo "❌ KAPI KIRMIZI ($TARGET) — artefakt ÜRETİLMEZ."
fi
exit $gate
