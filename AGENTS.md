# AGENTS.md — DUMB core

Public: protokol, sunucu (cmd/dumb-server), worker (cmd/dumb-worker), Auth arayüzü — Go. **Durum:** iskelet — Go modülü derleniyor, iki binary yalnızca sürüm basıyor; sıradaki: protokol (openapi) ve sunucunun kuyruk/long-poll uçları.

> **Oturum başı:** `.claude/session-commands.txt` dosyasını oku ve kullanıcıya ürünleri/skill'leri kısaca göster.

## Projeler

| Klasör | İçerik | Rol |
|---|---|---|
| `docs/` | Protocol and design docs (public, English). | — |
| `qa-automation/` | QA kapısı (none) | **AKTİF** — `qa-automation/README.md`'ye uy |

| `cmd/dumb-server/` | Kuyruk + long-poll uçları | **AKTİF** |
| `cmd/dumb-worker/` | Arka plan worker'ı (Ollama yöneticisi, self-update) | **AKTİF** |
| `internal/protocol/` | Wire tipleri — otorite `docs/protocol/openapi.yaml` | **AKTİF** |

Bu repo **public**. Buraya yazılan her şey herkes okuyormuş gibi güvenli olmalı; sır, adres, envanter ve production auth implementasyonu `../ops/`'a gider.


## Kaynak-doğruluk hiyerarşisi

1. **`docs/protocol/openapi.yaml`** — API sözleşme OTORİTESİ bu repo'dadır. Uç değişecekse önce sözleşme, sonra kod, aynı commit'te.
2. `SECURITY.md` — güvenlik tasarım kuralları.
3. Workspace `docs/adr/` — mimari kararlar.

## Ortak sabitler (workspace'ten — bilinçli tekrar)

- **DUMB = Distributed User-owned Model Backend.** README ve dokümanlarda açılım bu.
- **Public / private sınırı:** `core/` public (MIT). Sır, imzalama anahtarı, sunucu adresi, node envanteri ve production auth/pairing implementasyonu yalnız `ops/` (private) içinde. Güvenlik gizlilikle değil, sırlarla sağlanır: `core` içindeki her şey herkes okuyormuş gibi güvenli olmalı.
- **Worker'lar hiçbir bağlantı kabul etmez.** Yalnızca sunucuya giden HTTPS (long-poll). Ollama yalnız 127.0.0.1.
- **Go, standart kütüphane önce.** Sunucuda üçüncü taraf bağımlılık eklemek gerekçe ister (ADR).
- **Worker güncellemesi imzalı, atomik ve geri alınabilir.** İmzası doğrulanmayan binary asla uygulanmaz.
- Public repo içeriği (kod, yorum, README, commit) İngilizce.
- Sırlar ve test hesabı kimlikleri repo'ya YAZILMAZ; kimlikler `QA_` önekli env
  değişkenleriyle geçer. Testlerde satın alma/başvuru/ödeme gibi geri alınamaz işlem YAPILMAZ.

## Çalıştırma

```bash
go run ./cmd/dumb-server    # Yerel çalıştırma
go test ./...    # Birim testleri
go build ./...    # Artefakt
```

## QA otomasyonu

UI testi yok (qa=none — bilinçli karar, bkz. workspace ADR-0001). Kapı yalnız birim testlerini koşar.

## Skill'ler

| Skill | Ne yapar |
|---|---|
| `/core-build ` | Kapı yeşilse artefakt üretir ve DURUR (yüklemez/deploy etmez) |

### Bakım kuralı — yeni ekleme yaparken bunları da güncelle (ZORUNLU)

- **Yeni skill:** `.agents/skills/core-<ad>/SKILL.md` yaz (önek zorunlu) →
  cheat-sheet'e satır → bu tabloya satır → workspace kökünden symlink (`.agents/skills/<ad>` → `../../core/.agents/skills/<ad>`).
- **Yeni UI testi:** yalnız test dizinine dosya ekle — koşum script'i otomatik bulur,
  script'i ELLE değiştirme.
- **Build adımı/imza değişirken:** `core-build` skill'ini ve `build_cmd`'yi
  (`workspace.config.json`) güncel tut.

## Global kurallar (workspace'ten — bilinçli tekrar)

- **Yayınlama yalnız açık komutla.** Build artefakt üretir ve durur; push/deploy/store
  yükleme yalnız kullanıcının açık komutuyla.
- **Doğrulanabilir hedef.** Hata → önce onu üreten test. UI değişikliği → ekran görüntüsü.
  İkinci düzeltme de tutmadıysa üçüncü yama yok: kök neden.
- **Kayıt:** Bu dizinde açılan oturum workspace kök `CHANGELOG.md`'sine DOKUNMAZ; kayıt bu repo'nun git geçmişi ve `TODO.md`'sidir (her madde doğrulanabilir; yapılmayanlar da yazılır).
