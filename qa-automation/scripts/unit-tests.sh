#!/usr/bin/env bash
# Birim testleri — core. Komutlar workspace.config.json'daki unit_test_cmd'den
# üretildi; değişirse burayı VE config'i güncelle. Komut ürün kökünde koşar.
# Kullanım: ./unit-tests.sh (argüman yok)
# Çıkış: 0 geçti · 1 kırmızı · 3 yapılandırılmadı (kapıda KIRMIZI sayılır)
set -uo pipefail

PRODUCT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="${1:-all}"

# Paralel diziler (macOS bash 3.2 uyumlu — associative array yok)
LABELS=('default')
CMDS=('go test ./...')

rc=0; ran=0
for i in "${!LABELS[@]}"; do
  label="${LABELS[$i]}"; cmd="${CMDS[$i]}"
  if [[ "$TARGET" != "all" && "$label" != "default" && "$label" != "$TARGET" ]]; then continue; fi
  ran=1
  if [[ -z "$cmd" ]]; then
    echo "⚠️  [$label] birim test komutu YAPILANDIRILMADI (workspace.config.json → unit_test_cmd)."
    [[ $rc -eq 0 ]] && rc=3
    continue
  fi
  echo "▶ [$label] $cmd"
  if (cd "$PRODUCT_DIR" && bash -c "$cmd"); then
    echo "✅ [$label] birim testleri geçti"
  else
    echo "❌ [$label] birim testleri KIRMIZI"; rc=1
  fi
done
[[ $ran -eq 0 ]] && { echo "❌ '$TARGET' için tanımlı hedef yok."; exit 1; }
exit $rc
