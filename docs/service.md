# Running dumb-worker as a service

`dumb-worker service install|uninstall|print` writes and starts the service
definition for this OS. `print` shows what would be installed without touching
anything.

| OS | Kind | Runs as |
|---|---|---|
| macOS | launchd user agent (`~/Library/LaunchAgents/dumb-worker.plist`) | the logged-in user |
| Linux | systemd user unit (`~/.config/systemd/user/dumb-worker.service`) | the user |
| Linux, `-system` | systemd system unit (`/etc/systemd/system/dumb-worker.service`) | the dedicated `dumb` account |
| Windows | `sc.exe` service | LocalSystem (no SCM handling yet, see `TODO.md`) |

On macOS, `-log-dir` moves `dumb-worker.log` out of `~/Library/Logs`.

## macOS launchd user agent

Verified on a real Mac (Apple M4, macOS 27.0.1) on 2026-10-12 with
`QA_LAUNCHD=1 ./qa-automation/scripts/launchd-e2e.sh`, which installs the agent
from a temp dir and removes it again:

| Check | Result |
|---|---|
| `service install` → `launchctl print gui/<uid>/dumb-worker` | `state = running`, a pid, `runs = 1`; `plutil -lint` OK |
| caller job | served by the launchd-run worker (its job id in the agent's log) |
| `kill -9` | launchd starts a new pid (`last terminating signal = Killed: 9`, `runs = 2`), which serves again |
| 426 → self-update → exit 4 | `last exit code = 4`, `runs = 3`, 0.2.0 says hello and commits, serves again. The restart came ~9 s after the exit: `ThrottleInterval` 10 s counts from the process start, and the process had run only a few seconds |
| Gatekeeper | the binary written by the updater has no `com.apple.quarantine` (only `com.apple.provenance`), is ad-hoc linker-signed, and launchd runs it. `spctl --assess` says `rejected`, as for any un-notarized binary: Gatekeeper only assesses quarantined files at launch, and the updater downloads with Go's HTTP client, which sets no quarantine |
| `service uninstall` | `launchctl print` → `Could not find service`, plist gone, the worker said goodbye (status: nobody online) |

**Background Task Management.** macOS registers every agent in
`~/Library/LaunchAgents` as a background item: installing shows a "Background
Items Added" notification, and the user can switch `dumb-worker` off in
`System Settings > General > Login Items & Extensions` (then launchd will not
start it). Right after `service uninstall` the BTM record (`sfltool dumpbtm`, type
"legacy agent", name `dumb-worker`) was still listed, pointing at the deleted
plist; about three minutes later, with no login or reboot, macOS had pruned it
on its own. Never use `sfltool resetbtm` for this: it resets every app's
background items.

### How to verify after a reboot

Not testable by an agent; to be done by hand once:

1. From `core/`, build into a directory that survives a reboot (not `$TMPDIR`),
   with a fixed token pair (as `scripts/dev.sh` does, but kept):
   ```bash
   D=~/dumb-reboot-test; mkdir -p $D
   go build -o $D/dumb-server ./cmd/dumb-server
   go build -ldflags "-X main.version=0.0.1-dev" -o $D/dumb-worker ./cmd/dumb-worker
   W=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'); C=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
   (umask 077; printf %s "$W" > $D/token; echo "$C" > $D/caller-token)
   { echo "w1 worker $(printf %s "$W" | shasum -a 256 | cut -d' ' -f1)"; echo "app caller $(printf %s "$C" | shasum -a 256 | cut -d' ' -f1)"; } > $D/tokens
   DUMB_ADDR=127.0.0.1:18080 DUMB_TOKENS_FILE=$D/tokens $D/dumb-server &   # start this again after the reboot
   cd $D
   ```
2. `./dumb-worker service install -server http://127.0.0.1:18080 -token-file ~/dumb-reboot-test/token -models smollm2:135m -log-dir ~/dumb-reboot-test`
3. Check `launchctl print gui/$(id -u)/dumb-worker | grep -E 'state|pid'` → running.
4. Reboot and log in (a user agent starts at login, not at boot).
5. Without doing anything else: `launchctl print gui/$(id -u)/dumb-worker | grep -E 'state|pid|runs'`
   must show `state = running` with a new pid, and `~/dumb-reboot-test/dumb-worker.log`
   a fresh `hello:` line once the server is up (start it as in step 1); submit a job
   with `$D/caller-token` and see it served.
6. Also check `System Settings > General > Login Items & Extensions` lists
   `dumb-worker` as allowed, and `sfltool dumpbtm | grep -A8 'Name: dumb-worker'`.
7. Clean up: `./dumb-worker service uninstall`, stop the server, `rm -rf ~/dumb-reboot-test`.
   A few minutes later `sfltool dumpbtm | grep 'Name: dumb-worker'` should be empty.

## Linux system unit

The system unit runs as `User=dumb` / `Group=dumb`, never as root. Create the
account and give it the binary's directory first: self-update renames files next
to the binary (`<exe>.new`, `.prev`, `.update.json`, `.skip.json`, `.bad`; see
`release.md`), so that directory must be writable by `dumb` and is the only
writable path besides `/var/lib/dumb-worker`.

```bash
sudo useradd --system --user-group --home-dir /var/lib/dumb-worker --create-home --shell /usr/sbin/nologin dumb
sudo install -d -o dumb -g dumb -m 0755 /opt/dumb-worker      # binary dir: dumb owns it (self-update)
sudo install -o dumb -g dumb -m 0755 dumb-worker /opt/dumb-worker/
sudo install -d -o dumb -g dumb -m 0700 /etc/dumb-worker
sudo install -o dumb -g dumb -m 0600 token /etc/dumb-worker/token
sudo /opt/dumb-worker/dumb-worker service install -system \
  -server https://dumb.example.invalid -token-file /etc/dumb-worker/token -models smollm2:135m
```

Hardening in the unit, and why each one leaves the worker working:

| Directive | Effect |
|---|---|
| `NoNewPrivileges=yes`, `CapabilityBoundingSet=` (empty), `AmbientCapabilities=` | no capabilities, no setuid escalation; the worker needs none |
| `ProtectSystem=strict` + `ReadWritePaths=<binary dir>` | the whole file system is read-only except the binary directory (self-update) and `StateDirectory=dumb-worker` |
| `ProtectHome=read-only` | homes are visible but not writable (a token kept in a home still reads) |
| `PrivateTmp=yes`, `PrivateDevices=yes` | own `/tmp`, no device nodes; the worker uses neither |
| `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` | outbound HTTPS and loopback Ollama only |
| `ProtectKernel*`, `ProtectControlGroups`, `ProtectClock`, `ProtectHostname`, `RestrictNamespaces`, `RestrictSUIDSGID`, `RestrictRealtime`, `LockPersonality`, `MemoryDenyWriteExecute`, `SystemCallArchitectures=native` | standard sandboxing; a static Go binary needs none of what they block |
| `UMask=0077` | files the worker writes are private to `dumb` |

Not verified on a real Linux host yet (`systemd-analyze security` and a real
self-update under the unit); the generated unit is covered by
`TestSystemdSystemUnitHardening`.

## Models

The worker announces only the configured models (`-models`) that Ollama actually
has, re-checked every minute. When that set changes it says hello again. When
none is present it says goodbye (so callers get `503` at once instead of
waiting), stops claiming jobs, logs why, and only checks in with
`GET /v1/workers/me` (so a revoked credential or a required upgrade is still
noticed) until a model appears; then it says hello again.

## Shutdown

On SIGTERM / Ctrl-C (also `launchctl bootout`, `systemctl stop`), a drain before
an update restart, and a probation rollback, the worker finishes or fails its
jobs in flight, then calls `POST /v1/workers/goodbye`: the server stops counting
it online at once. Not after `401` / `426` (the server would refuse it too) or
`kill -9` (then the server notices after its online timeout, as before).
