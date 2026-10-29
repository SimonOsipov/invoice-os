#!/usr/bin/env bash
# nightly-ci.sh <ref>
# Dispatches ci.yml on <ref> and waits for the run to complete. Needs REPO; gh reads GH_TOKEN.
# Exit 0 on a completed run, whatever its conclusion; 1 on a dispatch failure, no run or a timeout; 2 on a usage error.
set -euo pipefail

if [ "$#" -lt 1 ] || [ -z "$1" ]; then
  echo "::error::usage: nightly-ci.sh <ref> (the ref is missing or empty)" >&2
  exit 2
fi
if [ -z "${REPO:-}" ]; then
  echo "::error::nightly-ci.sh: REPO (owner/name) is not set." >&2
  exit 2
fi
ref="$1"

since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
out=$(gh workflow run ci.yml -R "$REPO" --ref "$ref") || {
  echo "::error::could not dispatch ci.yml on $ref." >&2
  exit 1
}
echo "$out"

id=""
if [[ "$out" =~ /actions/runs/([0-9]+) ]]; then id="${BASH_REMATCH[1]}"; fi
if [ -z "$id" ]; then
  for _ in $(seq 1 12); do
    sleep 5
    runs=$(gh run list -R "$REPO" --workflow ci.yml --event workflow_dispatch --branch "$ref" --limit 10 \
      --json databaseId,createdAt 2>/dev/null || echo '[]')
    # ISO timestamps in one format compare as strings.
    id=$(jq -r --arg since "$since" '[.[]? | select(.createdAt >= $since)] | sort_by(.createdAt) | last | .databaseId // empty' <<< "$runs" 2>/dev/null || true)
    if [ -n "$id" ]; then break; fi
  done
fi
if [ -z "$id" ]; then
  echo "::error::no ci.yml run appeared for $ref after the dispatch." >&2
  exit 1
fi
echo "Dispatched CI run $id."
if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "run_id=$id" >> "$GITHUB_OUTPUT"; fi

for _ in $(seq 1 "${NIGHTLY_CI_POLLS:-120}"); do
  # A failed gh call is "not yet".
  status=$(gh run view "$id" -R "$REPO" --json status --jq .status 2>/dev/null || echo "")
  echo "  CI run $id: status=${status:-unknown}"
  if [ "$status" = "completed" ]; then exit 0; fi
  sleep 30
done
echo "::error::CI run $id did not complete in time; cancelling it." >&2
gh run cancel "$id" -R "$REPO" || true
exit 1
