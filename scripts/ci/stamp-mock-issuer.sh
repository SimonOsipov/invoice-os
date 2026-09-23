#!/usr/bin/env sh
# Stamps mockissuer into cmd/gateway/build.tags, which the Dockerfile passes to go build -tags.
# Non-production builds only: the committed file stays empty.
set -eu

target="cmd/gateway/build.tags"

# Refuse rather than create: a missing file means the Dockerfile's tag input moved.
[ -f "$target" ] || { echo "::error::$target is missing; the Dockerfile reads the gateway's build tags from it." >&2; exit 1; }

printf 'mockissuer\n' > "$target"
echo "stamped $target = mockissuer"
