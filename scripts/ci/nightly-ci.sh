#!/usr/bin/env bash
# nightly-ci.sh <ref> [<workflow>=<output>]...   (default: ci.yml=run_id)
# Dispatches each workflow on <ref>, writes <output>=<run id> to $GITHUB_OUTPUT and waits for every run to complete. Needs REPO; gh reads GH_TOKEN.
# Exit 0 when every run completed, whatever its conclusion; 1 on a dispatch failure, no run or a timeout (every incomplete run is cancelled); 2 on a usage error.
set -euo pipefail

if [ "$#" -lt 1 ] || [ -z "$1" ]; then
  echo "::error::usage: nightly-ci.sh <ref> [<workflow>=<output>]... (the ref is missing or empty)" >&2
  exit 2
fi
if [ -z "${REPO:-}" ]; then
  echo "::error::nightly-ci.sh: REPO (owner/name) is not set." >&2
  exit 2
fi
ref="$1"
shift
if [ "$#" -eq 0 ]; then set -- ci.yml=run_id; fi
for pair in "$@"; do
  if [[ ! "$pair" =~ ^[^=]+=[^=]+$ ]]; then
    echo "::error::usage: nightly-ci.sh: '$pair' is not <workflow>=<output>." >&2
    exit 2
  fi
done

workflows=()
ids=()
done_=()
cancel_incomplete() {
  local i
  if [ "${#ids[@]}" -eq 0 ]; then return 0; fi
  for i in "${!ids[@]}"; do
    if [ "${done_[$i]}" != 1 ]; then gh run cancel "${ids[$i]}" -R "$REPO" || true; fi
  done
}

for pair in "$@"; do
  wf="${pair%%=*}"
  outname="${pair#*=}"
  # Runs that exist before the dispatch; the fallback skips them, so no clock compare is needed.
  before=$(gh run list -R "$REPO" --workflow "$wf" --event workflow_dispatch --branch "$ref" --limit 30 \
    --json databaseId 2>/dev/null || echo '[]')
  out=$(gh workflow run "$wf" -R "$REPO" --ref "$ref") || {
    echo "::error::could not dispatch $wf on $ref." >&2
    cancel_incomplete
    exit 1
  }
  echo "$out"

  id=""
  if [[ "$out" =~ /actions/runs/([0-9]+) ]]; then id="${BASH_REMATCH[1]}"; fi
  if [ -z "$id" ]; then
    for _ in $(seq 1 12); do
      sleep 5
      runs=$(gh run list -R "$REPO" --workflow "$wf" --event workflow_dispatch --branch "$ref" --limit 30 \
        --json databaseId 2>/dev/null || echo '[]')
      id=$(jq -r --argjson before "$before" '[.[]? | select(.databaseId as $i | $before | map(.databaseId) | index($i) | not)] | map(.databaseId) | max // empty' <<< "$runs" 2>/dev/null || true)
      if [ -n "$id" ]; then break; fi
    done
  fi
  if [ -z "$id" ]; then
    echo "::error::no $wf run appeared for $ref after the dispatch." >&2
    cancel_incomplete
    exit 1
  fi
  echo "Dispatched $wf run $id."
  if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "$outname=$id" >> "$GITHUB_OUTPUT"; fi
  workflows+=("$wf")
  ids+=("$id")
  done_+=(0)
done

for _ in $(seq 1 "${NIGHTLY_CI_POLLS:-120}"); do
  pending=0
  for i in "${!ids[@]}"; do
    if [ "${done_[$i]}" = 1 ]; then continue; fi
    # A failed gh call is "not yet".
    status=$(gh run view "${ids[$i]}" -R "$REPO" --json status --jq .status 2>/dev/null || echo "")
    echo "  ${workflows[$i]} run ${ids[$i]}: status=${status:-unknown}"
    if [ "$status" = "completed" ]; then done_[$i]=1; else pending=1; fi
  done
  if [ "$pending" = 0 ]; then exit 0; fi
  sleep 30
done
echo "::error::a dispatched run did not complete in time; cancelling the incomplete ones." >&2
cancel_incomplete
exit 1
