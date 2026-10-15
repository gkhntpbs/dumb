# Remote Ollama on the private network (opt-in)

By default `dumb-worker` talks to Ollama on loopback only and manages that
Ollama (install on macOS/Windows, start, model pulls). The explicit opt-in
`-ollama-remote` (`DUMB_OLLAMA_REMOTE=1`) lets a headless worker, e.g. a
Raspberry Pi, use an Ollama that runs on another machine of the **same private
network** instead:

```sh
dumb-worker run -ollama-remote -ollama http://ollama-host.lan:11434 -models qwen3:0.6b ...
dumb-worker install -user -ollama-remote -ollama http://10.0.0.5:11434 -models qwen3:0.6b ...
```

## What is allowed

The host must be, or resolve **only** to, private addresses:

| Allowed | Refused |
|---|---|
| IPv4 `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (also as IPv4-mapped IPv6) | public addresses, shared/CGNAT `100.64.0.0/10`, documentation ranges |
| IPv6 unique local `fc00::/7` | loopback (`127.0.0.0/8`, `::1`, `localhost`, `*.localhost`): drop the opt-in to use the local Ollama |
| | link-local `169.254.0.0/16`, `fe80::/10` (self-assigned, needs a zone, says nothing about which network the peer is on), unspecified, multicast, broadcast |
| | URLs with credentials, query or fragment; schemes other than `http`/`https` |

The check happens twice: when the URL is configured (IP literals; names are
checked syntactically) and **on every new connection**. The worker resolves the
name itself, refuses the connection if any answer is not private (a mixed answer
is refused outright), and dials the validated IP, so a DNS answer that changes
to a public or loopback address later (rebinding) is refused. Environment
proxies are ignored in remote mode, and redirects are never followed.

## What the worker does not do

A remote Ollama belongs to its own host. The worker never installs, starts,
stops or restarts it and never pulls a model to it (`/api/pull` is refused in
remote mode). `setup` and the run-time readiness recovery only check that the
API answers and that the configured models are present; a missing model fails
with "pull it there". When the remote Ollama is unreachable the worker stops
advertising its models (goodbye), retries with the bounded recovery backoff
(1 min doubling to 10 min) and says hello again once it answers. `doctor` skips
the local install and exposure checks and reports the remote version.

## Risks

- Ollama speaks plain HTTP: prompts and outputs cross the LAN unencrypted and
  are visible to anyone who can observe that network. Use it only on a network
  you trust; an `https` reverse proxy in front of the remote Ollama is accepted.
- The remote Ollama listens beyond loopback by necessity, so every device on the
  network can use it. Restrict it with the host firewall to the worker's address.
- The worker cannot tell a genuine Ollama from another service at that address;
  outputs are only as trustworthy as that host.

The desktop (macOS/Windows) saved configuration remains loopback-only. Without
the opt-in, behaviour is exactly as before. The decision is recorded in the
workspace ADR-0018.

## Tests

`go test ./internal/ollama -run Remote` (address classes, opt-in, URL refusals,
dial-time pinning, mixed and rebinding answers, recovery that never pulls or
starts, management refused), `go test ./internal/doctor -run Remote`,
`go test ./cmd/dumb-worker -run 'OllamaRemote|Install'`.
