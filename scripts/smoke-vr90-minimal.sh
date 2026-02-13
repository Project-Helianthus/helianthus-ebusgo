#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

go test ./emulation -run '^TestSmokeVR90(MinimalQuerySet|B509DiscoveryQuerySet|MappedCommandQuerySet)$' -count=1 -v "$@"
