#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

go test ./emulation -run '^TestSmokeVR90MinimalQuerySet$' -count=1 -v "$@"
