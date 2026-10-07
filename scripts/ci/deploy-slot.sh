#!/usr/bin/env bash
# deploy-slot.sh - waits until fewer than 2 other runs hold a Railway deploy slot. Needs REPO, RUN_ID, EVENT_NAME; gh reads GH_TOKEN.
# Exit 0 when the slot is taken, 1 on a timeout, 2 on a usage error.
set -euo pipefail

SLOTS=2
POLL_SECONDS=60
DEADLINE_SECONDS=2400
# ceiling: runs older than 3 h are never fetched; raise it if a deploy chain can outlive that
MAX_AGE_SECONDS=10800
# ceiling: a holder stops counting 60 min after its slot passed; raise it if a healthy chain runs longer
MAX_HOLD_SECONDS=3600
NO_SLOT_GRACE_SECONDS=60

for v in REPO RUN_ID EVENT_NAME; do
  if [ -z "${!v:-}" ]; then
    echo "::error::usage: deploy-slot.sh needs REPO, RUN_ID and EVENT_NAME (env); $v is missing or empty." >&2
    exit 2
  fi
done

if [ "$EVENT_NAME" != "pull_request" ]; then
  echo "Deploy slot: a $EVENT_NAME run takes a slot without waiting."
  exit 0
fi

errf=$(mktemp)
trap 'rm -f "$errf"' EXIT

# fetch <url> <jq type test>: sets body; sets err and returns 1 on a gh failure or a wrong body.
fetch() {
  if ! body=$(gh api "$1" 2>"$errf"); then
    err=$(head -n 1 "$errf")
    return 1
  fi
  if ! printf '%s' "$body" | jq -e "$2" >/dev/null 2>&1; then
    err="not a JSON object"
    return 1
  fi
}

# classify: reads a jobs body; prints one verdict word.
classify() {
  jq -r --argjson now "$1" --argjson id "$2" --argjson self "$RUN_ID" \
    --argjson hold "$MAX_HOLD_SECONDS" --argjson grace "$NO_SLOT_GRACE_SECONDS" '
    def job($n): [.jobs[] | select(.name == $n)][0];
    def age($j): $now - ($j.completed_at | fromdateiso8601);
    job("Deploy slot") as $slot | job("Release deploy slot") as $rel | job("Detect E2E-relevant changes") as $chg
    | if $rel != null and $rel.status == "completed" then "settled"
      elif $slot == null then
        (if $chg != null and $chg.completed_at != null and age($chg) >= $grace then "settled" else "none" end)
      elif $slot.status != "completed" then (if $id < $self then "waiter" else "none" end)
      elif $slot.conclusion != "success" then "settled"
      elif age($slot) >= $hold then "expired"
      else "holder" end' 2>/dev/null || echo unreadable
}

start=$(date +%s)
settled=" "
last_held="none"
k=0
while :; do
  k=$((k + 1))
  now=$(date +%s)
  elapsed=$((now - start))
  held="" held_n=0 waiting="" waiting_n=0 expired="" unreadable="" problems=""

  # a jq error on the body (e.g. a null timestamp) is an unreadable body, not a crash
  if fetch "repos/$REPO/actions/workflows/dev-env.yml/runs?per_page=100" '.workflow_runs | type == "array"' &&
    { candidates=$(printf '%s' "$body" | jq -r --argjson now "$now" --argjson self "$RUN_ID" --argjson maxage "$MAX_AGE_SECONDS" '
      [.workflow_runs[] | select(.status != "completed" and .id != $self and ($now - (.run_started_at | fromdateiso8601)) <= $maxage)]
      | sort_by(.id)[]
      | [.id, .run_attempt, (if (.pull_requests | length) > 0 then "PR #\(.pull_requests[0].number)" else "\(.event) \(.head_branch)" end)]
      | @tsv' 2>"$errf") || { err=$(head -n 1 "$errf"); false; }; }; then
    while IFS=$'\t' read -r id attempt label; do
      [ -n "$id" ] || continue
      case "$settled" in *" $id:$attempt "*) continue ;; esac
      if ! fetch "repos/$REPO/actions/runs/$id/jobs?filter=latest&per_page=100" '.jobs | type == "array"'; then
        problems="${problems:+$problems; }could not read run $id: $err"
        unreadable="${unreadable:+$unreadable, }run $id"
        continue
      fi
      case "$(printf '%s' "$body" | classify "$now" "$id")" in
        holder) held="${held:+$held, }run $id ($label)"; held_n=$((held_n + 1)) ;;
        waiter) waiting="${waiting:+$waiting, }run $id ($label)"; waiting_n=$((waiting_n + 1)) ;;
        expired) expired="${expired:+$expired, }run $id ($label)" ;;
        settled) settled="$settled$id:$attempt " ;;
        unreadable)
          problems="${problems:+$problems; }could not read run $id: unreadable jobs body"
          unreadable="${unreadable:+$unreadable, }run $id" ;;
      esac
    done <<< "$candidates"
  else
    problems="could not read the runs: $err"
  fi

  if [ -n "$problems" ]; then
    echo "Deploy slot: poll $k: $problems; waiting $POLL_SECONDS s."
  else
    last_held="${held:-none}"
    line="Deploy slot: poll $k: $held_n of $SLOTS held"
    [ -z "$held" ] || line="$line by $held"
    [ "$waiting_n" -eq 0 ] || line="$line; $waiting_n older waiting: $waiting"
    [ -z "$expired" ] || line="$line; expired: $expired"
    echo "$line"
    if [ $((held_n + waiting_n)) -lt "$SLOTS" ]; then
      echo "Deploy slot: taken after $elapsed s."
      exit 0
    fi
  fi

  if [ $((elapsed + POLL_SECONDS)) -ge "$DEADLINE_SECONDS" ]; then
    msg="::error::Deploy slot: no free slot after $k polls ($elapsed s); held by $last_held"
    [ -z "$unreadable" ] || msg="$msg; unreadable: $unreadable"
    echo "$msg" >&2
    exit 1
  fi
  # ceiling: no per-call timeout; a hung gh call ends at the job's 50 min limit, without the holder list
  sleep "$POLL_SECONDS"
done
