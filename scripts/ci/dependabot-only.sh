#!/usr/bin/env bash
# Decide whether Dependabot alone wrote a PR. Usage: dependabot-only.sh <pr-number>
# Test: TestDependabotOnly_MissingEnvExits2.
# ceiling: public repo; the call needs pull-requests: read if the repo goes private
set -euo pipefail
pr=${1:-}
for v in GH_TOKEN REPO PR_AUTHOR EVENT_SENDER EVENT_ACTION HEAD_SHA GITHUB_OUTPUT; do
  if [ -z "${!v:-}" ] && [ "$v" != EVENT_ACTION ]; then
    echo "dependabot-only: $v missing" >&2
    exit 2
  fi
done
if [ -z "$pr" ] || [ -z "${EVENT_ACTION+x}" ]; then
  echo "dependabot-only: argument or EVENT_ACTION missing" >&2
  exit 2
fi
bot='dependabot[bot]'
raw=$(mktemp)
trap 'rm -f "$raw"' EXIT

count=0
verdict=false
if gh api --paginate "repos/$REPO/pulls/$pr/commits?per_page=100" >"$raw" 2>/dev/null \
  && result=$(jq -rs --arg head "$HEAD_SHA" --arg bot "$bot" '
    (add // []) as $c
    | if ($c | length) == 0 or ($c | last | .sha) != $head then "unreliable"
      elif all($c[]; .author.login == $bot and .committer.login == "web-flow" and .commit.verification.verified == true) then "true \($c | length)"
      else "false \($c | length)" end' "$raw"); then
  verdict=${result% *}
  count=${result#* }
elif [ "$PR_AUTHOR" = "$bot" ]; then
  echo "::error::dependabot-only: commit lookup failed for PR $pr" >&2
  exit 1
else
  echo "::warning::dependabot-only: commit lookup failed for PR $pr"
fi

if [ "$PR_AUTHOR" = "$bot" ] && [ "$verdict" = unreliable ]; then
  echo "::error::dependabot-only: commit list empty or stale for PR $pr" >&2
  exit 1
fi
if [ "$PR_AUTHOR" != "$bot" ] || [ "$EVENT_SENDER" != "$bot" ] \
  || { [ "$EVENT_ACTION" != opened ] && [ "$EVENT_ACTION" != synchronize ]; }; then
  verdict=false
fi
[ "$verdict" = true ] || verdict=false
echo "dependabot_only=$verdict" >>"$GITHUB_OUTPUT"
echo "dependabot-only: $verdict (author=$PR_AUTHOR sender=$EVENT_SENDER action=$EVENT_ACTION commits=$count)"
