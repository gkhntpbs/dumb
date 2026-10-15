<p align="center">
  <img src="docs/brand/dumb-bear-icon-160.png" width="160" height="160" alt="The DUMB bear, a sleepy brown bear">
</p>

```text
     .-"""-.                         .-"""-.
    /  .-.  \   _.-----------._   /  .-.  \
   |  (   )  |-'`               `'-|  (   )  |
    \  '-'  /                       \  '-'  /
     '-._.-'                         '-._.-'
      /                                   \
     /                                     \
    |                                       |
    |     '-.___.-'           '-.___.-'     |
    |              .-"""""""-.              |
    |            .'   _____   '.            |
    |           /    (     )    \           |
    |          |      '-.-'      |          |
     \         |        |        |         /
      \         \     \___/     /         /
       '.        '.           .'        .'
         '-.       '-._____.-'       .-'
            '-._                 _.-'
                '--._________.--'

           ____    _   _   __  __   ____
          |  _ \  | | | | |  \/  | | __ )
          | | | | | | | | | |\/| | |  _ \
          | |_| | | |_| | | |  | | | |_) |
          |____/   \___/  |_|  |_| |____/

       Distributed User-owned Model Backend
```

# DUMB — Distributed User-owned Model Backend

Run AI requests on machines you own, wherever they are, and fall back to the cloud when
none of them are online.

- **Pull, not push.** Workers long-poll the server over outbound HTTPS. No open ports, no
  tunnels, no VPN; a worker behind any home NAT just works.
- **Ollama does the inference.** The worker is a small background daemon that manages a
  local Ollama (installs it or uses the existing one) and talks to it on `127.0.0.1` only
  (an explicit opt-in allows an Ollama elsewhere on the private network: `docs/remote-ollama.md`).
- **Fallback.** When no worker answers in time, the caller takes its cloud path instead.
- **Signed, atomic, reversible updates** from GitHub Releases.

Status: early design. Nothing here is usable yet.

## Layout

```
cmd/dumb-server   job queue + long-poll endpoints
cmd/dumb-worker   background worker
internal/protocol wire types (authority: docs/protocol/openapi.yaml)
scripts/dev.sh    local server + worker for development
```

## Local development

```bash
./scripts/dev.sh
```

Builds `dumb-server` and `dumb-worker`, starts both on `127.0.0.1` against your local
Ollama (model `smollm2:135m` by default, `DEV_MODELS` to change), with throwaway tokens
in a temp dir. It prints the caller token and an example `curl`; Ctrl-C stops everything
and deletes the temp dir.

## Security

See [SECURITY.md](SECURITY.md). This repository is written to be safe while fully public:
secrets, signing keys and deployment details are never part of it.

## License

MIT

## macOS menu bar

Build a local menu-only application with `./scripts/build-macos-app.sh`. It uses
a native accessory helper with graceful Pause, Resume and Quit; no application
windows or Dock icon. Configuration still uses the existing flags/environment
and token file; double-clicking an unconfigured bundle is not onboarding.
Unbundled macOS development/CLI runs explicitly use `-headless`. See
[macos-menu-bar.md](docs/macos-menu-bar.md) for lifecycle, verification and
pending signed distribution/visual acceptance.
