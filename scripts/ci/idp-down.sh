#!/usr/bin/env bash
# scripts/ci/idp-down.sh: removes the containers idp-up.sh started with the same IDP_SLOT. Safe to run when none exist.
set -uo pipefail
sfx=""
if [ "${IDP_SLOT:-0}" != 0 ]; then sfx="-s$IDP_SLOT"; fi
docker rm -f "idp-es256$sfx" "idp-hs256$sfx" "idp-rebuild$sfx" "idp-mail$sfx" "idp-short$sfx" "mailpit$sfx" >/dev/null 2>&1 || true
