# Raspberry Pi acceptance: remote LAN Ollama, Linux install, recommended version — 2026-10-15

Real run on a Raspberry Pi 4 (Debian GNU/Linux 13 trixie, aarch64, 4 cores, 3.7 GiB,
`/bin/sh` = dash, OpenSSL 3.5.7) against an Ollama 0.35.0 on another machine of the
same home network. Private addresses are redacted here (`<lan-ollama-host>` is an
RFC 1918 address); the unredacted record is in the private `ops` repository.

Isolation: everything ran under `~/dumb-accept-20261015` (mode 0700) on the Pi; the
only file outside it was the systemd **user** unit `dumb-worker-accept.service`,
removed afterwards. No global install, no system service, no `sudo`, no model pull.
`dumb-server` listened on `127.0.0.1:18080` with throwaway fixture tokens; a
`python3 -m http.server` on `127.0.0.1:18081` served an empty release list as the
worker's update source. The binaries were cross-built on the development Mac
(`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`, version `0.3.0-accept.1`, a throwaway
update key) from core `2da064b`; nothing was built on the Pi.

```
88ded193ff67b72fdfd1d69b3f5c9446f6c0452d8639c321eec3dfb20cecd220  dist/dumb-server
7332707f2e99f71c94fbadf81ade629012d357adb77b6d113552f7f71b356da2  dist/dumb-worker
617b3317ad0118ab198a3e9400ea10f0219d5f4de791f5b05314f50c9af72768  dist/install.sh
7332707f2e99f71c94fbadf81ade629012d357adb77b6d113552f7f71b356da2  install/bin/dumb-worker
```

## Results

| Check | Result |
|---|---|
| `install.sh --version 0.2.0 --download-only` against the real published GitHub release, production public key, under dash | PASS: signature and archive checksum verified, `dumb-worker 0.2.0` extracted |
| Same with a public key whose key id differs by one byte | PASS: refused ("signature was made with another key"), nothing extracted |
| `doctor` with a private URL and no opt-in | PASS: refused, "needs the explicit -ollama-remote opt-in" |
| `doctor -ollama-remote` with a public IP, loopback, link-local `169.254.169.254` | PASS: each refused before any connection |
| `doctor -ollama-remote` with `example.com` (resolves to public addresses) | PASS: refused at dial time ("resolves to 2606:…") |
| `doctor -ollama-remote` against the LAN Ollama | PASS: all 8 checks; local install/exposure checks skipped; sample generation 3688 ms |
| `dumb-worker install -user …` (pre-issued token, remote Ollama) | PASS: binary, 0600 token in 0700 dir, user unit written, enabled, active |
| Worker hello / model advertisement from the remote Ollama | PASS: `hello: … models [smollm2:135m]` |
| Server-recommended version (`DUMB_RECOMMENDED_WORKER_VERSION=0.9.0`) | PASS: `/v1/workers/me` returns it; the worker ran an immediate check "update (server recommends 0.9.0)" right after the startup check (2 requests to the release source) |
| Real job | PASS: `state done` with output, HTTP 200, 118 ms on the worker |
| Cancel of a leased job | PASS: cancelled 46 ms after the claim; the worker's next ack was refused; "lease revoked; dropped"; final state `cancelled` |
| Re-run of the same install | PASS: "unchanged", no restart (same MainPID) |
| Install with a model absent on the remote Ollama | PASS: refused ("pull it there"), no pull, unit and running service untouched |
| Clean-up | PASS: `service uninstall -service-name dumb-worker-accept`, processes stopped, directory and the (previously absent) `~/.config` removed; home entries, running services and listening ports match the pre-test snapshot. The worker's journald lines remain in the system journal. |

Not covered on the Pi: an actual install from a recommendation (no newer signed
release exists; covered by unit tests and `update-e2e.sh`), `-system` mode and
pairing (no root use and no HTTPS pairing server on the Pi; unit tests cover them),
and the remote Ollama becoming unreachable (it is the user's machine and was not
disturbed; unit tests cover recovery).

## install.sh (real release)

```
dumb-worker 0.2.0 for linux/arm64
signature: ok (dumb v0.2.0 checksums.txt)
archive: ok (b1f5aec01776ae705b8fb8ff502a1ffeabb3e9c421b2591978d8bead9747685c)
verified binary: ./gh-verified/dumb-worker
dumb-worker 0.2.0 for linux/arm64
install.sh: signature was made with another key
```

## doctor

```
### 1 private URL without opt-in
[ok  ] Token file: ~/dumb-accept-20261015/worker-token
[FAIL] Ollama URL: ollama: "http://<lan-ollama-host>:11434" is not a loopback address; Ollama must only be used on 127.0.0.1 (a private-network Ollama needs the explicit -ollama-remote opt-in)
exit=1
### 2 public IP with opt-in
[ok  ] Token file: ~/dumb-accept-20261015/worker-token
[FAIL] Ollama URL: ollama: remote host is not on a private network: "http://8.8.8.8:11434" (allowed: 10/8, 172.16/12, 192.168/16, fc00::/7)
exit=1
### 3 loopback with opt-in
[ok  ] Token file: ~/dumb-accept-20261015/worker-token
[FAIL] Ollama URL: ollama: "http://127.0.0.1:11434" is loopback; drop -ollama-remote to use the local Ollama
exit=1
### 4 link-local metadata address with opt-in
[ok  ] Token file: ~/dumb-accept-20261015/worker-token
[FAIL] Ollama URL: ollama: remote host is not on a private network: "http://169.254.169.254" (allowed: 10/8, 172.16/12, 192.168/16, fc00::/7)
exit=1
### 5 name resolving to public addresses, with opt-in (checked at dial time)
[ok  ] Token file: ~/dumb-accept-20261015/worker-token
[ok  ] Ollama installed: remote Ollama on the private network; installed and managed on its own host
[FAIL] Ollama reachable: ollama: Get "http://example.com:11434/api/version": ollama: remote host is not on a private network: example.com resolves to 2606:4700:10::6814:179a
[ok  ] Ollama not exposed: not checked in remote mode: the remote Ollama listens on the private network by design; keep that network trusted
[FAIL] Model smollm2:135m: cannot list models: ollama: Get "http://example.com:11434/api/tags": ollama: remote host is not on a private network: example.com resolves to 2606:4700:10::ac42:93f3
[ok  ] Server reachable: http://127.0.0.1:18080 answered HTTP 404
[ok  ] Credential and version: accepted as worker pi-accept (version 0.3.0-accept.1, minimum 0.1.0)
[FAIL] Sample latency: ollama: Post "http://example.com:11434/api/generate": ollama: remote host is not on a private network: example.com resolves to 2606:4700:10::6814:179a
exit=1
### 6 Mac mini on the LAN with opt-in
[ok  ] Token file: ~/dumb-accept-20261015/worker-token
[ok  ] Ollama installed: remote Ollama on the private network; installed and managed on its own host
[ok  ] Ollama reachable: version 0.35.0 on a private-network host (remote mode)
[ok  ] Ollama not exposed: not checked in remote mode: the remote Ollama listens on the private network by design; keep that network trusted
[ok  ] Model smollm2:135m: present
[ok  ] Server reachable: http://127.0.0.1:18080 answered HTTP 404
[ok  ] Credential and version: accepted as worker pi-accept (version 0.3.0-accept.1, minimum 0.1.0)
[ok  ] Sample latency: 3688 ms on smollm2:135m
exit=0
```

## install

```
-user -dir ~/dumb-accept-20261015/install -service-name dumb-worker-accept -server http://127.0.0.1:18080 -models smollm2:135m -ollama-remote -ollama http://<lan-ollama-host>:11434 -update-url http://127.0.0.1:18081 -token-file ~/dumb-accept-20261015/worker-token
### first install
Checking Ollama and the models…
dumb-worker: 2026/10/15 05:20:40 install: running systemctl --user daemon-reload
dumb-worker: 2026/10/15 05:20:40 install: running systemctl --user enable --now dumb-worker-accept.service
Created symlink '~/.config/systemd/user/default.target.wants/dumb-worker-accept.service' → '~/.config/systemd/user/dumb-worker-accept.service'.
dumb-worker: 2026/10/15 05:20:40 install: running systemctl --user is-active --quiet dumb-worker-accept.service
dumb-worker installed: dumb-worker-accept.service is running.
  binary     ~/dumb-accept-20261015/install/bin/dumb-worker
  credential ~/dumb-accept-20261015/install/token
  unit       ~/.config/systemd/user/dumb-worker-accept.service
  logs       journalctl --user -u dumb-worker-accept.service -f
Note: a user unit stops when you log out and does not start at boot unless lingering is on:
  sudo loginctl enable-linger <user>
exit=0
### re-run, same arguments
Checking Ollama and the models…
dumb-worker: 2026/10/15 05:21:32 install: running systemctl --user daemon-reload
dumb-worker: 2026/10/15 05:21:33 install: running systemctl --user enable --now dumb-worker-accept.service
dumb-worker: 2026/10/15 05:21:33 install: running systemctl --user is-active --quiet dumb-worker-accept.service
dumb-worker unchanged: dumb-worker-accept.service is running.
  binary     ~/dumb-accept-20261015/install/bin/dumb-worker
  credential ~/dumb-accept-20261015/install/token
  unit       ~/.config/systemd/user/dumb-worker-accept.service
  logs       journalctl --user -u dumb-worker-accept.service -f
Note: a user unit stops when you log out and does not start at boot unless lingering is on:
  sudo loginctl enable-linger <user>
exit=0
MainPID before=2387074 after=2387074
### model absent on the remote Ollama
Checking Ollama and the models…
dumb-worker: 2026/10/15 05:21:33 Ollama recovery attempt 1/3 failed: ollama: configured model missing on the remote Ollama (pull it there): dumb-absent-model:1b
dumb-worker: 2026/10/15 05:21:33 install: Ollama is not ready (ollama: configured model missing on the remote Ollama (pull it there): dumb-absent-model:1b); fix it and run install again, or pass -skip-setup to start the service anyway
exit=1
```

## Worker journal

```
dumb-worker: 2026/10/15 05:20:41 version 0.3.0-accept.1, server http://127.0.0.1:18080, channel stable
dumb-worker: 2026/10/15 05:20:41 update (startup): update: no newer release in this channel
dumb-worker: 2026/10/15 05:20:41 update (server recommends 0.9.0): update: no newer release in this channel
dumb-worker: 2026/10/15 05:20:41 hello: worker pi-accept, models [smollm2:135m], capacity 1, max wait 25s
dumb-worker: 2026/10/15 05:21:16 job 4bbbe0a7d352f548ca1d9bba2739e9b0: done in 118ms
dumb-worker: 2026/10/15 05:21:16 job e8e7a27edb3e64996fed25ccf58554c5: lease revoked; dropped
```

## Jobs

```
### real job (wait 60s)
{"job_id":"4bbbe0a7d352f548ca1d9bba2739e9b0","state":"done","output":"Oh, I'm sorry to hear about your recent situation. Please, if you"}
 http=200
### cancel a leased job
attempt 1: job e8e7a27edb3e64996fed25ccf58554c5 was leased; cancel http=200; final {"job_id":"e8e7a27edb3e64996fed25ccf58554c5","state":"cancelled"}
LEASED_CANCEL_JOB=e8e7a27edb3e64996fed25ccf58554c5
```

## Server log

```
{"time":"2026-10-15T05:18:59.131547417+03:00","level":"INFO","msg":"dumb-server starting","version":"0.3.0-accept.1","protocol":1,"addr":"127.0.0.1:18080"}
{"time":"2026-10-15T05:20:41.093768813+03:00","level":"INFO","msg":"worker hello","worker":"pi-accept","worker_version":"0.3.0-accept.1","models":["smollm2:135m"],"capacity":1,"features":["ack"]}
{"time":"2026-10-15T05:21:16.344481334+03:00","level":"INFO","msg":"job submitted","job_id":"4bbbe0a7d352f548ca1d9bba2739e9b0","caller":"accept-caller","model":"smollm2:135m","prompt_bytes":30,"system_bytes":0,"deadline_ms":60000}
{"time":"2026-10-15T05:21:16.344851921+03:00","level":"INFO","msg":"job claimed","job_id":"4bbbe0a7d352f548ca1d9bba2739e9b0","worker":"pi-accept","model":"smollm2:135m","prompt_bytes":30,"system_bytes":0,"lease_expires_at":"2026-10-15T05:22:16.344416002+03:00","ack":true}
{"time":"2026-10-15T05:21:16.462708142+03:00","level":"INFO","msg":"job result","job_id":"4bbbe0a7d352f548ca1d9bba2739e9b0","worker":"pi-accept","state":"done","output_bytes":65,"error_bytes":0}
{"time":"2026-10-15T05:21:16.484928986+03:00","level":"INFO","msg":"job submitted","job_id":"e8e7a27edb3e64996fed25ccf58554c5","caller":"accept-caller","model":"smollm2:135m","prompt_bytes":52,"system_bytes":0,"deadline_ms":60000}
{"time":"2026-10-15T05:21:16.485183315+03:00","level":"INFO","msg":"job claimed","job_id":"e8e7a27edb3e64996fed25ccf58554c5","worker":"pi-accept","model":"smollm2:135m","prompt_bytes":52,"system_bytes":0,"lease_expires_at":"2026-10-15T05:22:16.484872061+03:00","ack":true}
{"time":"2026-10-15T05:21:16.531433066+03:00","level":"INFO","msg":"job cancelled","job_id":"e8e7a27edb3e64996fed25ccf58554c5","caller":"accept-caller"}
{"time":"2026-10-15T05:21:16.737712837+03:00","level":"INFO","msg":"ack refused","job_id":"e8e7a27edb3e64996fed25ccf58554c5","worker":"pi-accept","err":"lease is not valid"}
```

## Release-source requests

```
127.0.0.1 - - [15/Oct/2026 05:19:01] "GET /repos/gkhntpbs/dumb/releases HTTP/1.1" 200 -
127.0.0.1 - - [15/Oct/2026 05:20:41] "GET /repos/gkhntpbs/dumb/releases?per_page=50 HTTP/1.1" 200 -
127.0.0.1 - - [15/Oct/2026 05:20:41] "GET /repos/gkhntpbs/dumb/releases?per_page=50 HTTP/1.1" 200 -
```
