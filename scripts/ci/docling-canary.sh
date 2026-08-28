#!/usr/bin/env bash
# scripts/ci/docling-canary.sh <healthz|buildsha|models|nonroot> [args...]
#
# Assertion steps for ci.yml's docling-canary job (T-02-1..4). Every check execs
# into the already-running `docling-canary` container and asks it in python3:
# --network none has no eth0, so a curl from the runner or a `docker run -p`
# mapping can never reach it, and python3 is the one tool guaranteed present in
# a slim image (it is the app's own runtime) -- curl/find/grep may not be.
#
# DOCLING_CANARY_PORT defaults to 8080, the fleet's $PORT convention
# (internal/platform/config.go); confirm it matches whatever port subtask 02's
# Dockerfile actually binds and override the job's env if not.
set -euo pipefail

CONTAINER="${DOCLING_CANARY_CONTAINER:-docling-canary}"
PORT="${DOCLING_CANARY_PORT:-8080}"

check="${1:?usage: docling-canary.sh <healthz|buildsha|models|nonroot> [args...]}"
shift

healthz_json() {
  docker exec "$CONTAINER" python3 -c "
import json, urllib.request
print(json.dumps(json.load(urllib.request.urlopen('http://localhost:${PORT}/healthz', timeout=2))))
"
}

case "$check" in
healthz)
  # T-02-2: 200 within 15s of container start.
  deadline=$((SECONDS + 15))
  until healthz_json >/tmp/docling-healthz.json 2>/dev/null; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "::error::/healthz did not answer within 15s of container start"
      exit 1
    fi
    sleep 1
  done
  echo "/healthz answered: $(cat /tmp/docling-healthz.json)"
  ;;

buildsha)
  # T-02-4: build equals the sha stamp-build-sha.sh wrote before this image was built.
  want="${1:?usage: docling-canary.sh buildsha <sha>}"
  got="$(healthz_json | python3 -c 'import json, sys; print(json.load(sys.stdin)["build"])')"
  if [ "$got" != "$want" ]; then
    echo "::error::/healthz build=$got, want $want"
    exit 1
  fi
  echo "/healthz build matches the stamped sha ($got)"
  ;;

models)
  # T-02-1: layout, TableFormer and RapidOCR-en are all present, each over 1
  # MiB. Substring match on the on-disk path is the only oracle available
  # before subtask 02 bakes a real image -- tighten once the real layout is
  # known.
  docker exec "$CONTAINER" python3 -c "
import os, sys
root = os.environ.get('DOCLING_ARTIFACTS_PATH', '')
if not root:
    print('DOCLING_ARTIFACTS_PATH is unset', file=sys.stderr); sys.exit(1)
big = []
for dirpath, _, files in os.walk(root):
    for name in files:
        p = os.path.join(dirpath, name)
        if os.path.getsize(p) > 1024 * 1024:
            big.append(p.lower())
if not big:
    print(f'no file over 1 MiB found under {root}', file=sys.stderr); sys.exit(1)
missing = [n for n in ('layout', 'tableformer', 'rapidocr') if not any(n in p for p in big)]
if missing:
    print(f'no file over 1 MiB under {root} matches {missing}', file=sys.stderr)
    print(chr(10).join(big), file=sys.stderr)
    sys.exit(1)
print(f'layout, TableFormer and RapidOCR-en models present and >1 MiB under {root} ({len(big)} file(s))')
"
  ;;

nonroot)
  # T-02-3: id -u is non-zero.
  uid="$(docker exec "$CONTAINER" id -u)"
  if [ "$uid" = "0" ]; then
    echo "::error::container runs as root (uid 0)"
    exit 1
  fi
  echo "container runs as uid $uid (non-root)"
  ;;

*)
  echo "::error::unknown check '$check'"
  exit 1
  ;;
esac
