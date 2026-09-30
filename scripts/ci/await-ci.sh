#!/usr/bin/env bash
# await-ci.sh [--once] <sha>
# Polls the `CI` check-run on <sha> every 15 s, 80 times. Needs REPO; gh reads GH_TOKEN.
# Exit 0 on success, 1 on other conclusions or a timeout, 2 on usage. --once: one poll, exit 1 only on a completed non-success.
set -euo pipefail

once=0
if [ "${1:-}" = "--once" ]; then once=1; shift; fi
if [ "$#" -lt 1 ] || [ -z "$1" ]; then
  echo "::error::usage: await-ci.sh <sha> (the sha is missing or empty)" >&2
  exit 2
fi
if [ -z "${REPO:-}" ]; then
  echo "::error::await-ci.sh: REPO (owner/name) is not set." >&2
  exit 2
fi
sha="$1"

emit() {
  if [ "$once" = 1 ]; then return 0; fi
  if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "conclusion=$1" >> "$GITHUB_OUTPUT"; fi
}

echo "Gating deploy on the 'CI' check for $sha ..."
for _ in $(seq 1 80); do
  # A failed gh call or a body that is not a JSON object is "no verdict yet".
  j=$(gh api "repos/$REPO/commits/$sha/check-runs?check_name=CI&per_page=100" 2>/dev/null || echo '{}')
  echo "$j" | jq -e 'type == "object"' >/dev/null 2>&1 || j='{}'
  status=$(echo "$j" | jq -r '[.check_runs[]?] | sort_by(.started_at // "") | last | .status // "none"')
  concl=$(echo "$j" | jq -r '[.check_runs[]?] | sort_by(.started_at // "") | last | .conclusion // "none"')
  echo "  CI: status=$status conclusion=$concl"
  if [ "$concl" = "success" ]; then
    emit "$concl"
    echo "CI is green."
    exit 0
  fi
  if [ "$status" = "completed" ] && [ "$concl" != "none" ]; then
    emit "$concl"
    echo "::error::CI concluded '$concl' for $sha." >&2
    exit 1
  fi
  if [ "$once" = 1 ]; then exit 0; fi
  sleep 15
done
echo "::error::Timed out after ~20m waiting for the CI check on $sha." >&2
exit 1
