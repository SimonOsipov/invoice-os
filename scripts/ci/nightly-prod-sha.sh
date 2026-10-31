#!/usr/bin/env bash
# Writes main's head (head) and its latest first-parent commit that changes more than *.md (expected).
# deploy-prod.yml ignores **.md, so a docs-only head never reaches production.
# ceiling: no wait for a deploy in flight, add one if a red night traces to one.
set -euo pipefail

: "${GITHUB_OUTPUT:?}"

git fetch --filter=blob:none origin main
head=$(git rev-parse origin/main)
expected=$(git log -1 --format=%H --first-parent origin/main -- ':!*.md')
[ -n "$expected" ] || { echo "::error::no code commit on main"; exit 1; }

printf 'head=%s\nexpected=%s\n' "$head" "$expected" >>"$GITHUB_OUTPUT"
