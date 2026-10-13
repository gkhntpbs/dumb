#!/usr/bin/env bash
set -euo pipefail
CORE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export GOMAXPROCS=2 GOMEMLIMIT=256MiB GOFLAGS=-p=1 GOPROXY=off GOSUMDB=off
cd "$CORE"
exec go run ./cmd/admin-preview
