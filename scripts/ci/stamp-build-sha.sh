#!/usr/bin/env sh
# scripts/ci/stamp-build-sha.sh <sha>
#
# Writes the commit under test into internal/platform/buildsha.txt so it travels
# inside the tarball `railway up` uploads and is compiled into every Go service.
# /healthz then reports it and GET /healthz/fleet aggregates it per service,
# which is what lets the deploy gates block until the fleet is genuinely serving
# THIS commit.
#
# Why this exists: `railway up --ci` returns after the BUILD, and /healthz
# answers 200 from the OLD container for the whole rolling deploy. "Fleet green"
# therefore never meant "fleet is running this commit" — measured 2026-08-03,
# where the E2E suite ran to completion against the previous commit's backend and
# a destructive PR-environment reset landed in the middle of it.
#
# POSIX sh (not bash): runs inside the minimal ghcr.io/railwayapp/cli container.
set -eu

sha="${1:?usage: stamp-build-sha.sh <sha>}"
target="internal/platform/buildsha.txt"

[ -f "$target" ] || { echo "::error::$target is missing; the go:embed in internal/platform/buildsha.go would not compile." >&2; exit 1; }

printf '%s\n' "$sha" > "$target"
echo "stamped $target = $sha"
