# Security

Please report vulnerabilities privately through GitHub's "Report a vulnerability"
(Security Advisories) on this repository, not in a public issue.

Design rules this project holds itself to:

- Workers accept no inbound connections.
- Every worker has its own revocable credential; only its hash is stored server-side.
- Worker binaries are applied only after their signature verifies against a key
  embedded at build time (minisign over `checksums.txt`, then the archive's SHA-256;
  see `docs/release.md`). A new version that cannot reach the server is rolled back.
- Every principal is rate limited (in-memory token buckets), and failed
  authentication is limited per client IP: once an IP's bucket is empty its
  requests get `429` before their token is checked. `X-Forwarded-For` is only
  believed from configured trusted proxies (`DUMB_TRUSTED_PROXIES`).
- Job status and result lookup require the authenticated submitting caller
  principal. Another caller receives the same immediate 404 as an unknown job.
- Queue admission bounds active/retained job counts and accounted payload bytes
  globally and per caller; accepted jobs reserve their result budget. See
  `docs/queue-admission.md` for limits and accounting scope.
- Logs never contain tokens, prompts or outputs, only their sizes.
- The Linux system unit runs as a dedicated `dumb` user under systemd sandboxing
  (`docs/service.md`).
- Nothing in this repository depends on being secret.
