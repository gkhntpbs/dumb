---
name: core-build
description: DUMB core — builds the release artefact if the QA gate is green, then STOPS (no upload, no deploy, no push).
---
# /core-build

No arguments. Commands run from the repository root (`core/`) — if called from the workspace
root, `cd core` first.

Build the release artefact. **HARD RULE:** this skill BUILDS and **STOPS**. No release
upload, no deploy, no `git push`, no tag push — only on the user's explicit, separate command.

## 1. Gate (mandatory, deterministic)
```bash
./qa-automation/scripts/build-gate.sh $ARGUMENTS
```
Order: clean → unit tests, with `QA_STRICT=1`.
- **Non-zero exit → BUILD CANCELLED.** Do not build; report the red step and its diagnosis, stop.
- Missing tests / not configured / missing env = RED. Partially green is not green.

## 2. Artefact
```bash
go build ./...
```
Confirm the output exists (path + size); for a signed artefact, verify the signature.
- On a signing or credential error, never report success; say exactly what is missing.
- If the version needs raising, **ask** — never raise it automatically.

## 3. Report and stop

| Target | Artefact | Version | Size | Status |
|---|---|---|---|---|
| … | `…` | … | … | ✅/❌ |

Closing sentence (verbatim): **"Artefact ready. The upload/deploy step is yours — I did not do it."**
