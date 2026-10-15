# Raspberry Pi: published v0.3.0 `install.sh`, `-system` mode and pairing — 2026-10-15

Real run on a Raspberry Pi 4 (Debian GNU/Linux 13 trixie, aarch64, 3.7 GiB, OpenSSL 3.5.7)
against the operator's production server over HTTPS and an Ollama 0.35.0 on another machine
of the same home network. Server address, LAN addresses and the worker ID are redacted
(`<server>`, `<lan-ollama-host>`); the unredacted record is in the private `ops` repository.
This closes the earlier Pi run's gaps (`pi-remote-ollama-2026-10-15.md`): `-system` mode,
pairing, and the one-command path from a published release.

Nothing was built on the Pi. The binary is the published release asset.

```
617b3317ad0118ab198a3e9400ea10f0219d5f4de791f5b05314f50c9af72768  install.sh  (= git show v0.3.0:scripts/install.sh)
63727a5ffd18fd78e613eeff8a12a0f52c7260c48cba569dcb84333c620b46fe  /opt/dumb-worker/dumb-worker  (= dumb-worker_0.3.0_linux_arm64.tar.gz member)
```

## Install

A one-time pairing code (10 minute TTL) was created through the admin-manage API and
placed in a root-only 0600 file; it was never printed or passed on a command line.

```
sudo sh ./install.sh --version 0.3.0 -system -server https://<server> -models smollm2:135m \
  -ollama-remote -ollama http://<lan-ollama-host>:11434 -pair-code-file /root/<code-file>

dumb-worker 0.3.0 for linux/arm64
signature: ok (dumb v0.3.0 checksums.txt)
archive: ok (f1a3f699fe9b610a4d7c4f6561dcfb26c8912ac0d4af70df05a8dc4286510e4a)
dumb-worker: install: running useradd --system --user-group --home-dir /var/lib/dumb-worker --create-home --shell /usr/sbin/nologin dumb
Paired: worker credential saved.
Checking Ollama and the models…
dumb-worker: install: running systemctl daemon-reload
dumb-worker: install: running systemctl enable --now dumb-worker.service
Created symlink '/etc/systemd/system/multi-user.target.wants/dumb-worker.service' → '/etc/systemd/system/dumb-worker.service'.
dumb-worker: install: running systemctl is-active --quiet dumb-worker.service
dumb-worker installed: dumb-worker.service is running.
  binary     /opt/dumb-worker/dumb-worker
  credential /etc/dumb-worker/token
  unit       /etc/systemd/system/dumb-worker.service
  logs       journalctl -u dumb-worker.service -f
```

The code file was deleted afterwards.

## Results

| Check | Result |
|---|---|
| Published `install.sh` verifies the minisign signature and archive checksum, then runs `dumb-worker install` | PASS |
| `-system`: user `dumb` created (system, nologin); `/opt/dumb-worker` dumb 0755, binary dumb 0755; `/etc/dumb-worker` dumb 0700, `token` dumb 0600 | PASS |
| Unit `User=dumb`, `enabled` + `active`; environment has no secret (`DUMB_TOKEN_FILE` path only) | PASS |
| `systemd-analyze security dumb-worker.service` on the real host | 3.1 "OK" |
| Pairing over HTTPS (one-time code → worker credential) | PASS |
| Journal: `version 0.3.0`, startup update check "no newer release", hello with `[smollm2:135m]` from the remote Ollama | PASS |
| Server recommends 0.3.0 (`GET /v1/workers/me` → `recommended_worker_version: "0.3.0"`) | no additional check: equal to own version (no-op) |
| Admin worker list | 1 worker, `worker_version 0.3.0`, `online-idle` |
| Real caller job through the public API | `done`, nonempty, 1.0 s end to end; worker "done in 613ms" |
| Cancel of a leased job | cancel 200 `cancelled`, repeat 200, GET 410 `cancelled`; the worker had finished first, its result was refused (409) and discarded |
| `systemctl restart dumb-worker` | graceful stop, new process said hello within 1 s, next job `done` in 0.7 s |
| Host before/after | only `dumb-worker.service` added (running + enabled); listening sockets unchanged (the worker listens on nothing); other services untouched |

The worker was left installed and running (operator decision).

## Not covered / follow-ups

- No reboot; start at boot relies on `enabled` (multi-user.target).
- A real self-update under the hardened unit (no newer release exists yet).
- The remote Ollama becoming unreachable (the operator's machine).
- `useradd --create-home` leaves `/var/lib/dumb-worker` at 0755 with `/etc/skel` files. It holds no
  secret (the credential is in `/etc/dumb-worker`), but a 0700 home without skel would be tighter.
