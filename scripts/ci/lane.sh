#!/usr/bin/env bash
# lane.sh - decides the lane of a PR (pr, pr-e2e, classify) and the deploy scope of a push to main (push <sha>).
# Writes the verdict to stdout and $GITHUB_OUTPUT. push needs REPO, RUN_ID and gh (GH_TOKEN). Exit 0, 1 on a bad FILTER_E2E, 2 on a usage error.
set -euo pipefail

LIBRARY_PREFIX='frontend/library/'
GATEWAY_JOB_PREFIX='Deploy gateway '
FLEET_GATE_JOB_PREFIX='Fleet /healthz gate'
SPA_GATE_JOB_PREFIX='SPA build gate'
# ceiling: only the newest 50 main push runs are read; raise per_page if a full deploy is ever older than that
RUNS_URL_QUERY='event=push&branch=main&per_page=50'

usage() {
  echo "::error::usage: lane.sh classify | pr | pr-e2e | push <sha> (push also needs REPO and RUN_ID in env)" >&2
  exit 2
}

# emit <key=value>...: stdout first, $GITHUB_OUTPUT as the last action.
emit() {
  printf '%s\n' "$@"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf '%s\n' "$@" >> "$GITHUB_OUTPUT"
  fi
}

# classify: paths on stdin; library only when a path is read and every path starts with the library prefix.
classify() {
  local path seen=0
  while IFS= read -r path || [ -n "$path" ]; do
    [ -n "$path" ] || continue
    seen=1
    case "$path" in
      "$LIBRARY_PREFIX"*) ;;
      *) echo full; return 0 ;;
    esac
  done
  if [ "$seen" = 1 ]; then echo library; else echo full; fi
}

# pr_lane: the lane of the merge commit's diff against its first parent; any git failure is full.
pr_lane() {
  local paths
  if ! paths=$(git diff --no-renames --name-only HEAD^1 HEAD 2>/dev/null); then
    echo full
    return 0
  fi
  printf '%s\n' "$paths" | classify
}

# push_scope <sha>: sets scope and reason.
push_scope() {
  local sha=$1 runs ids id jobs base="" base_run="" prefixes paths
  scope=full
  if ! runs=$(gh api "repos/$REPO/actions/workflows/dev-env.yml/runs?$RUNS_URL_QUERY" 2>&1); then
    reason="runs lookup failed: $(printf '%s' "$runs" | head -n 1)"
    return 0
  fi
  if ! ids=$(printf '%s' "$runs" | jq -r --arg self "$RUN_ID" \
    '.workflow_runs | map(select(.event == "push" and .head_branch == "main" and (.id | tostring) != $self))
     | sort_by(.run_started_at) | reverse | .[] | "\(.id) \(.head_sha)"' 2>/dev/null); then
    reason="runs body is not JSON"
    return 0
  fi
  prefixes=$(jq -n --arg g "$GATEWAY_JOB_PREFIX" --arg f "$FLEET_GATE_JOB_PREFIX" --arg s "$SPA_GATE_JOB_PREFIX" '[$g, $f, $s]')
  local head
  while read -r id head; do
    [ -n "$id" ] || continue
    if ! jobs=$(gh api "repos/$REPO/actions/runs/$id/jobs?filter=latest&per_page=100" 2>&1); then
      reason="jobs lookup failed for run $id: $(printf '%s' "$jobs" | head -n 1)"
      return 0
    fi
    if ! printf '%s' "$jobs" | jq -e --argjson p "$prefixes" \
      '.jobs as $j | $p | all(. as $pre | ($j | map(select(.name | startswith($pre)))) as $m | ($m | length) > 0 and ($m | all(.conclusion == "success")))' >/dev/null 2>&1; then
      continue
    fi
    base=$head
    base_run=$id
    break
  done <<< "$ids"
  if [ -z "$base" ]; then
    reason="no push run with a successful gateway deploy, fleet gate and SPA build gate"
    return 0
  fi
  if ! git merge-base --is-ancestor "$base" "$sha" 2>/dev/null; then
    reason="base ${base:0:7} from run $base_run is not an ancestor of ${sha:0:7}"
    return 0
  fi
  if ! paths=$(git diff --no-renames --name-only "$base" "$sha" 2>/dev/null); then
    reason="git diff failed from base ${base:0:7}"
    return 0
  fi
  if [ "$(printf '%s\n' "$paths" | classify)" = library ]; then
    scope=library
  fi
  reason="base ${base:0:7} from run $base_run"
}

cmd=${1:-}
case "$cmd" in
  classify)
    classify
    ;;
  pr)
    emit "lane=$(pr_lane)"
    ;;
  pr-e2e)
    lane=$(pr_lane)
    case "${FILTER_E2E-}" in
      true) if [ "$lane" = library ]; then e2e=false; else e2e=true; fi ;;
      false) e2e=false ;;
      *)
        echo "::error::pr-e2e: FILTER_E2E must be true or false, got '${FILTER_E2E-}'" >&2
        exit 1
        ;;
    esac
    emit "lane=$lane" "e2e=$e2e"
    ;;
  push)
    sha=${2:-}
    for v in sha REPO RUN_ID; do
      if [ -z "${!v:-}" ]; then
        echo "::error::usage: lane.sh push <sha> needs REPO and RUN_ID (env); $v is missing or empty." >&2
        exit 2
      fi
    done
    push_scope "$sha"
    echo "$reason"
    emit "scope=$scope"
    ;;
  *) usage ;;
esac
