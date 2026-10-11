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
none is present it stops claiming jobs, logs why, and only checks in with
`GET /v1/workers/me` (so a revoked credential or a required upgrade is still
noticed) until a model appears.
