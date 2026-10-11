# QA otomasyonu — DUMB core (none)

Sözleşme: setup-workspace `references/qa-gate.md`. Kısa hâli:

| Script | Görev |
|---|---|
| `scripts/run-all-tests.sh` | UI testlerini otomatik bulur ve koşar → `results/<ts>/` |
| `scripts/unit-tests.sh` | Birim testleri |
| `scripts/clean-results.sh [--dry-run]` | Çıktıları temizler |
| `scripts/build-gate.sh` | temizle → birim → UI (`QA_STRICT=1`); build'in kapısı |

## Dizin

- UI testi yok (qa=none). Yalnız `scripts/unit-tests.sh` + `scripts/build-gate.sh`.

## Yeni test yazmak

UI testi kapsam dışı (qa=none). Birim testleri ürünün kendi test düzenindedir.

## Kurallar

- Yeni test = yeni dosya; koşum script'i ELLE değiştirilmez.
- Kimlik bilgisi `QA_` önekli env ile; dosyanın başında `requires-env:` satırı.
- Satın alma/başvuru/ödeme/mesaj gibi geri alınamaz işlem YOK.
- Seçici canlı UI ağacından/DOM'dan doğrulanır, ekran görüntüsünden tahmin edilmez.
- `results/` git'e girmez.

## Bilinen tuzaklar

Öğrenildikçe buraya eklenir (izin diyalogları, onboarding ekranları, klavye/submit
davranışı, animasyon beklemeleri…). Teşhiste ilk bakılacak yer burasıdır.

## CI

Henüz yok — `build-gate.sh` CI'da aynen koşacak tek giriş noktasıdır.
