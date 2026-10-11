# Security

Please report vulnerabilities privately through GitHub's "Report a vulnerability"
(Security Advisories) on this repository, not in a public issue.

Design rules this project holds itself to:

- Workers accept no inbound connections.
- Every worker has its own revocable credential; only its hash is stored server-side.
- Worker binaries are applied only after their signature verifies against a key
  embedded at build time (minisign over `checksums.txt`, then the archive's SHA-256;
  see `docs/release.md`). A new version that cannot reach the server is rolled back.
- Nothing in this repository depends on being secret.
