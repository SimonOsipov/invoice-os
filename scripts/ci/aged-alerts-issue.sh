#!/usr/bin/env bash
# Open, update or close the one aged-alert issue. Usage: aged-alerts-issue.sh <body-file>
# Test: TestAgedAlertsScript_ListFailureWritesNothing, TestAgedAlertsScript_NewAlertOnOpenIssueAddsOneComment.
set -euo pipefail
body_file=${1:-}
if [ -z "$body_file" ] || [ ! -f "$body_file" ]; then
  echo "aged-alerts-issue: body file missing: ${body_file}" >&2
  exit 2
fi
list=$(gh issue list --state all --limit 100 --search "in:title \"$ISSUE_TITLE\"" --json number,title,state,body)
found=$(jq -r --arg t "$ISSUE_TITLE" 'map(select(.title == $t)) | sort_by(.number) | last // empty | "\(.number) \(.state)"' <<<"$list")
number=${found% *}
state=${found#* }
if [ ! -s "$body_file" ]; then
  if [ "$state" = OPEN ]; then
    gh issue close "$number" --comment "No Dependabot alert is open more than 7 days."
  fi
  exit 0
fi
body=$(mktemp)
current=$(mktemp)
comment=$(mktemp)
trap 'rm -f "$body" "$current" "$comment"' EXIT
{
  cat "$body_file"
  echo
  echo "Last checked by run $GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID."
} > "$body"
if [ -z "$found" ]; then
  gh issue create --title "$ISSUE_TITLE" --body-file "$body"
  exit 0
fi
if [ "$state" = CLOSED ]; then
  gh issue reopen "$number"
fi
if [ "$state" = OPEN ]; then
  jq -r --argjson n "$number" '.[] | select(.number == $n) | .body // ""' <<<"$list" > "$current"
  # Rows of the new body whose alert number is in no row of the current body.
  fresh=$(awk -F'|' 'FILENAME == ARGV[1] { if ($2 ~ /^ #[0-9]+ $/) seen[$2] = 1; next }
    $2 ~ /^ #[0-9]+ $/ && !($2 in seen)' "$current" "$body_file")
  if [ -n "$fresh" ]; then
    {
      if [ -n "${NOTIFY:-}" ]; then echo "@$NOTIFY"; echo; fi
      echo "New Dependabot alert open more than 7 days:"
      echo
      echo "| Alert | Package | GHSA | Severity | Age (days) | Link |"
      echo "|---|---|---|---|---|---|"
      echo "$fresh"
    } > "$comment"
    gh issue comment "$number" --body-file "$comment"
  fi
fi
gh issue edit "$number" --body-file "$body"
echo "Updated issue #$number."
