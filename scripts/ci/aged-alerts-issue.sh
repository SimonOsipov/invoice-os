#!/usr/bin/env bash
# Open, update or close the one aged-alert issue. Usage: aged-alerts-issue.sh <body-file>
# pipefail: a failed `gh issue list` must not read as "no issue" and create a duplicate.
set -euo pipefail
body_file=${1:-}
if [ -z "$body_file" ] || [ ! -f "$body_file" ]; then
  echo "aged-alerts-issue: body file missing: ${body_file}" >&2
  exit 2
fi
found=$(gh issue list --state all --limit 100 --search "in:title \"$ISSUE_TITLE\"" --json number,title,state \
  | jq -r --arg t "$ISSUE_TITLE" 'map(select(.title == $t)) | sort_by(.number) | last // empty | "\(.number) \(.state)"')
number=${found% *}
state=${found#* }
if [ ! -s "$body_file" ]; then
  if [ "$state" = OPEN ]; then
    gh issue close "$number" --comment "No Dependabot alert is open more than 7 days."
  fi
  exit 0
fi
body=$(mktemp)
trap 'rm -f "$body"' EXIT
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
gh issue edit "$number" --body-file "$body"
echo "Updated issue #$number."
