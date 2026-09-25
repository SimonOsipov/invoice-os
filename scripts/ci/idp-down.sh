#!/usr/bin/env bash
# scripts/ci/idp-down.sh: removes the containers idp-up.sh started. Safe to run when none exist.
set -uo pipefail
docker rm -f idp-es256 idp-hs256 idp-rebuild idp-mail mailpit >/dev/null 2>&1 || true
