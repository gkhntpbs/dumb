---
name: core-build
description: DUMB core — QA kapısı yeşilse yayın artefaktını üretir ve DURUR (yüklemez, deploy etmez, push etmez).
---
# /core-build

Hedef: (argüman yok). Komutlar ürün kökünde (`core/`) koşar — workspace kökünden çağrıldıysan önce `cd core`.

Yayın artefaktını üret. **KESİN KURAL:** Bu skill artefaktı ÜRETİR ve **DURUR**. Store'a
yükleme, sunucuya deploy, `git push`, sürüm etiketi push'u YAPILMAZ — yalnız kullanıcının
açık, ayrı komutuyla.

## 1. KAPI (zorunlu, deterministik)
Gerekli `QA_` kimliklerini env ile geçirip ürün kökünden:
```bash
./qa-automation/scripts/build-gate.sh $ARGUMENTS
```
Sıra: temizle → birim testleri → UI testleri (none), `QA_STRICT=1` ile.
- **Çıkış kodu ≠ 0 → BUILD İPTAL.** Artefakt üretme; kırmızı adımı ve teşhisi raporla, dur.
- Cihaz yok / test yok / yapılandırılmadı / env eksik = KIRMIZI. Kısmi yeşil yeşil değildir.

## 2. Artefakt
```bash
go build ./...
```
Çıktının üretildiğini doğrula (dosya yolu + boyut); imzalı artefaktta imzayı doğrula.
- İmza/kimlik bilgisi hatası olursa uydurma başarı raporlama; eksik olanı net söyle
  (yeri: workspace `secrets/README.md`).
- Sürüm numarası artırmak gerekiyorsa **sor** — otomatik artırma.

## 3. Rapor + dur

| Hedef | Artefakt | Sürüm | Boyut | Durum |
|---|---|---|---|---|
| … | `…` | … | … | ✅/❌ |

Kapanış cümlesi (aynen): **"Artefakt hazır. Yükleme/deploy adımını sen yapacaksın —
otomatik yapmadım."**
