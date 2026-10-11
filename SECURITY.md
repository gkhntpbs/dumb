# Security

Please report vulnerabilities privately through GitHub's "Report a vulnerability"
(Security Advisories) on this repository, not in a public issue.

Design rules this project holds itself to:

- Workers accept no inbound connections.
- Every worker has its own revocable credential; only its hash is stored server-side.
- Worker binaries are applied only after their signature verifies against a key
  embedded at build time.
- Nothing in this repository depends on being secret.
