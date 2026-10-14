# Release preflight — 2026-10-15

The first proposed public version is `v0.2.0`. Local history has no existing
version release tags, only two local backup tags. Publication must push only the
reviewed main commit and the selected release tag, never those backup refs.

The release workflow now installs the same locked Node.js 26.0.0/npm 11.12.1
caller tooling as CI before the full build gate. GoReleaser is pinned to 2.18.3
in the workflow and local instructions. Draft creation, checksum signing,
signature verification and publication order remain unchanged.

The first real snapshot build compiled all eleven binaries but failed archive
creation: the Windows worker includes `dumb-tray.exe`, while other worker
platforms include only one executable. The worker archive now explicitly allows
this different binary count, using GoReleaser's documented
[`allow_different_binary_count`](https://goreleaser.com/customization/package/archives/)
setting. The second snapshot completed all ten archives and `checksums.txt`.
Archive names remain exactly those expected by `internal/update.ArchiveName`.

`qa-automation/scripts/check-release-archives.py` checked every archive's SHA256,
exact executable/README/LICENSE members, embedded version and expected production
public key. The Windows worker ZIP contains both worker and compatibility tray.
The snapshot version was `0.0.1-snapshot`, because no release tag existed; this
checks packaging without creating a tag or publishing an asset. It is not signed
release acceptance. No private key was provided to the public build.

The prior Windows cross-vet warning at `setup_console_windows.go:56` came from
converting GlobalLock's integer address into a Go pointer. The bounded clipboard
copy now uses the existing typed `windows.ReadProcessMemory` API while the block
is locked, then validates copied NUL-terminated UTF-16LE without retaining a
foreign pointer. The parser's initial red run failed because its helper did not
exist; valid Unicode, whitespace, missing terminator, odd/oversized buffers and
multiline cases now pass. Full native/Windows/Darwin vet passes. See Microsoft's
[ReadProcessMemory contract](https://learn.microsoft.com/en-us/windows/win32/api/memoryapi/nf-memoryapi-readprocessmemory).
Native Windows clipboard/console execution remains deferred at the user's
request; local parser and cross-vet evidence does not establish that acceptance.

The inherited transparent branding was reviewed separately before release:
light/dark preview, alpha 0–255 with transparent corners, and pixel equality to
the inherited PNG set. Pinned Pillow 12.0.0 normalized PNG compression bytes;
a second full regeneration changed no hashes. Public evidence contains only
source derivation, icon hashes and regeneration instructions; host installation
status and desktop binary inventory were removed before their first commit.
The native Explorer executable icon remains generic; the runtime tray uses its
transparent embedded ICO. Minisign authenticity does not supply OS notarization
or Authenticode signing.

Passed local commands:

```sh
GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 QA_OLLAMA=0 QA_LAUNCHD=0 ./qa-automation/scripts/build-gate.sh
GOMAXPROCS=2 GOFLAGS=-p=1 go vet ./...
GOMAXPROCS=2 GOFLAGS=-p=1 GOOS=windows GOARCH=amd64 go vet ./...
GOMAXPROCS=2 GOFLAGS=-p=1 GOOS=darwin GOARCH=amd64 go vet ./...
goreleaser check
goreleaser release --snapshot --clean --skip=publish --parallelism=2
```

The build receives only `DUMB_RELEASE_PUBLIC_KEY`; use the intended public key
when running the archive verifier. Snapshot/private production-key signing and
actual published-asset update/rollback acceptance are distinct steps.

Pinned tools were downloaded from official GitHub release assets and their
SHA256 values verified against published checksums:

| Tool | Archive | SHA256 |
| --- | --- | --- |
| Gitleaks 8.30.0 | `gitleaks_8.30.0_darwin_arm64.tar.gz` | `b251ab2bcd4cd8ba9e56ff37698c033ebf38582b477d21ebd86586d927cf87e7` |
| GoReleaser 2.18.3 | `goreleaser_Darwin_arm64.tar.gz` | `821c58ab8c31d57bb331cab9f1a703eb3673bff4fce0db9cc3c4a038af14f76f` |

The established [Gitleaks scanner](https://github.com/gitleaks/gitleaks/blob/v8.30.0/README.md)
found no secrets in all reachable history. Run the committed reusable
`GITLEAKS_BIN=/path/to/gitleaks ./qa-automation/scripts/public-source-audit.sh`
after the gate; it scans all history plus the current tracked tree and keeps
redacted reports in ignored QA output. Independent source/image review and
private-memory exact credential comparison were additionally performed outside
this public repository. Scanner success is evidence for this audited scope,
not a guarantee that arbitrary undiscovered secrets cannot exist.
