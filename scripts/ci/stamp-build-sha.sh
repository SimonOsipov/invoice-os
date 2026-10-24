#!/usr/bin/env sh
# scripts/ci/stamp-build-sha.sh <sha>
#
# Writes the commit under test into internal/platform/buildsha.txt, which is compiled
# into every Go service. /healthz reports it and /healthz/fleet aggregates it, so the
# deploy gates wait until the fleet serves THIS commit, not the previous revision.
set -eu

sha="${1:?usage: stamp-build-sha.sh <sha>}"
target="internal/platform/buildsha.txt"

[ -f "$target" ] || { echo "::error::$target is missing; the go:embed in internal/platform/buildsha.go would not compile." >&2; exit 1; }

printf '%s\n' "$sha" > "$target"
echo "stamped $target = $sha"
