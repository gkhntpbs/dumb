# Worker pairing

The optional credential-management provider can issue a one-time worker pairing
code. Obtain a code from an authorized administrator, then run:

```sh
dumb-worker pair -server https://your-server.example -token-file /private/path/token
```

Enter the code at the hidden stdin prompt. Do not put it in shell arguments,
command history, environment variables or logs. An automation may supply one
line through a protected stdin pipe; the command does not echo it. The command
accepts only `-server` and `-token-file`, without environment defaults. HTTPS
certificate verification stays enabled; redirects and automatic retries are
refused. The exchange includes the worker version and bounds its response to
64 KiB and its request duration to 15 seconds.

The parent directory must already exist and be controlled by the user. Symlink
paths and special files are rejected. On Unix the parent must belong to the current user and cannot be writable by
other users; ancestors must belong to that user or root, with shared write
access permitted only on sticky directories. The staged file has mode 0600. Windows requires parent access to be
restricted to the process user, SYSTEM and Administrators and applies that ACL
to the staged file before writing any credential. The token is synced and
atomically replaces the configured file; it is never printed. Unix also syncs
the containing directory. Windows uses a write-through replacement. A response
or storage failure may occur after the code has been consumed: obtain a fresh
code rather than automatically retrying. A directory-sync failure may mean the
new token already occupies the path.

Pairing replaces only the selected credential file. Existing configuration,
models, Ollama, startup registration and running processes are untouched. Keep
`-server` consistent with the existing configuration when renewing a credential.
The worker reads the token at startup: restart that worker through its own
tray/menu or service after pairing. Re-pairing does not invoke the installer or
bypass its configuration guard. Creating a code alone does not retire an old
credential; successful exchange does. Existing pre-issued-token setup remains
available. The native setup UI has no pairing selection in this release.

Local acceptance: `go test -count=1 ./cmd/dumb-worker -run TestPair` exercises the
command dispatcher, TLS HTTP fixture, version header, hidden pipe input,
restricted atomic file replacement, bounded failure responses, redirects and
secret-safe errors. Native Windows console/ACL execution needs a Windows host;
cross-compilation does not establish that acceptance.
