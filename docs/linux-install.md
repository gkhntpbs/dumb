# One-command Linux install (VPS, Raspberry Pi)

`dumb-worker install` is a non-interactive install for headless Linux machines
(amd64 and arm64, e.g. a VPS or a Raspberry Pi 4/5 with a 64-bit OS). It builds
on `pair`, `setup`, `doctor` and `service install`:

1. installs the running binary (`<dir>/bin/dumb-worker`, or
   `/opt/dumb-worker/dumb-worker` for `-system`) atomically; an identical
   binary is left alone and a binary whose self-update is still on probation
   is never replaced;
2. stores the credential with mode 0600 in a 0700 directory, either by pairing
   with a one-time code or by copying a pre-issued token;
3. checks Ollama and the configured models (`setup`): with a local Ollama
   missing models are pulled; Ollama itself is not installed on Linux;
4. writes the systemd unit (the same unit as `service install`, always
   `DUMB_HEADLESS=1`), reloads systemd, enables and starts it, restarts it only
   when the binary, credential or unit changed, and checks that it is active.

It is safe to run again with the same arguments: unchanged parts are left
alone and an already stored credential is kept (a consumed pairing code is not
re-sent; pass `-force-pair` with a fresh code to replace the credential).

```sh
# Current account, systemd user unit (default): files under ~/.local/share/dumb-worker
dumb-worker install -user -server https://dumb.example -models qwen3:0.6b -pair-code-stdin

# Dedicated "dumb" account, hardened system unit (run as root)
sudo dumb-worker install -system -server https://dumb.example -models qwen3:0.6b -pair-code-file /root/pair-code
```

| Flag | Meaning |
|---|---|
| `-server`, `-models`, `-capacity`, `-ollama`, `-channel`, `-update-url` | As for `run` (same `DUMB_*` environment defaults) |
| `-name NAME` | Display name in the admin panel (env `DUMB_WORKER_NAME`, at most 64 characters, no control characters). Written into the unit only when given; otherwise the worker sends its hostname. Display only, never an identity |
| `-user` / `-system` | User unit for the current account (default; refused as root) / system unit as `User=dumb` (needs root; the account is created with `useradd --system` when missing) |
| `-dir DIR` | User install directory (default `~/.local/share/dumb-worker`) |
| `-service-name NAME` | Unit name (default `dumb-worker`); `service uninstall -service-name NAME` removes it |
| `-pair-code-file PATH`, `-pair-code-stdin`, `DUMB_PAIR_CODE` | One-time pairing code source (pairing requires an HTTPS server) |
| `-token-file PATH` | Copy a pre-issued worker token instead of pairing |
| `-force-pair` | Pair again even though a credential is stored |
| `-skip-setup` | Start the service even if Ollama or a model is not ready yet (the worker waits without claiming jobs) |

The pairing code comes from the admin panel: **Devices > Add worker** (new
worker) or **Re-pair worker** in a worker's details (same identity, add
`-force-pair`). The dialog shows this exact `install.sh` / `dumb-worker install
-system ... -pair-code-stdin` command with your server URL, models and optional
name filled in; paste the code when the command asks for it.

The pairing code never appears in arguments, logs or the unit. Prefer
`-pair-code-file` (delete the file afterwards) or `-pair-code-stdin` (hidden on
a terminal). `DUMB_PAIR_CODE` is accepted for automation but is visible to the
same user and root in the process environment while the command runs. A user
unit runs only while the account has a systemd user manager: without
`sudo loginctl enable-linger <user>` it stops at logout and does not start at
boot; the command prints this hint when lingering is off. Exit codes: 0 done,
1 failure (e.g. Ollama not ready: nothing is started), 2 configuration mistake
(nothing is touched).

## install.sh: download, verify, install

`scripts/install.sh` (POSIX `sh`) fetches the release archive for this machine
from GitHub Releases and verifies it before running anything from it:

1. `checksums.txt` and `checksums.txt.minisig` are downloaded; the minisign
   signature is checked with the release public key embedded in the script
   (prehashed Ed25519 over BLAKE2b-512, key id compared, trusted comment
   verified; legacy non-prehashed signatures are refused). OpenSSL 3 does the
   cryptography, so `minisign` need not be installed;
2. the archive's SHA-256 must match the signed checksum;
3. only then is `dumb-worker` extracted and `dumb-worker install` run with the
   remaining arguments.

```sh
curl -fsSLO https://raw.githubusercontent.com/gkhntpbs/dumb/main/scripts/install.sh
less install.sh                      # read it
sh install.sh -user -server https://dumb.example -models qwen3:0.6b -pair-code-stdin
sudo sh install.sh -system -server https://dumb.example -models qwen3:0.6b -pair-code-file /root/pair-code
sh install.sh --download-only ./verified     # verify and stop, no install
```

`--version X.Y.Z` pins a release (default: the latest stable). The `install`
command first ships in the release after v0.2.0; with v0.2.0 use
`--download-only` and the manual steps in `service.md`. Do not pipe the
script into `sh` when pairing from stdin: stdin would be the script. The
script's own trust root is wherever you fetched it from; compare it with the
tagged source if that matters to you. Requirements: `curl` or `wget`, `tar`,
`gzip`, `od`, `dd`, `awk`, `openssl` 3.0 or newer.

## Tests

- `go test ./cmd/dumb-worker -run Install` — fake systemd runner, chown,
  pairing and setup: fresh user and system installs, re-run idempotency, restart
  only on change, pairing sources, refusal of bad input before touching
  anything, probation and symlink protection.
- `go test ./internal/update -run InstallScript` — `install.sh` against a local
  mirror signed with a throwaway key: success, `--download-only`, and refusal
  (the binary never runs) for a replaced archive, edited checksums, another key,
  a key-id collision, a legacy signature, an edited trusted comment, a missing
  signature and an unsupported architecture. Needs OpenSSL 3 on `PATH`
  (skipped otherwise).
