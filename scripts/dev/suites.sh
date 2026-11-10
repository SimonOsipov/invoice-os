#!/usr/bin/env bash
# Runs Ralph's local suites in this worktree, one log per suite in .ralph/.
# Ralph runs it through `hm suite run`, which runs one worktree's suites at a time on the machine.
# Usage: [DEV_DB_PORT=<port>] scripts/dev/suites.sh   (no DEV_DB_PORT: the DB suites are skipped)
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
L=.ralph
mkdir -p "$L"

# CI's clean-clone job fails on these.
ignored=$(git ls-files -ci --exclude-standard 2>/dev/null)
if [ -n "$ignored" ]; then
  echo "ignored=1: tracked files match .gitignore (git rm --cached each); no suite ran"
  echo "$ignored"
  exit 1
fi

if ! scripts/dev/wait-go-test-idle.sh . > "$L/wait.log" 2>&1; then
  echo "wait=1: a go test still runs in this worktree; no suite ran"
  exit 1
fi

failed=0
fs=
suite() { # suite <name> <command...>
  local name=$1 rc
  shift
  "$@" > "$L/$name.log" 2>&1
  rc=$?
  echo "$name=$rc"
  [ "$rc" -eq 0 ] || { failed=1; fs="$fs $name"; }
}

# CI diffs against the PR base; origin/main stands in for it here.
base=
git rev-parse -q --verify origin/main > /dev/null 2>&1 && base=origin/main

# Packages with a changed or new .go file whose tests read a DATABASE_ env var.
# ceiling: changed packages only, not their importers; widen if CI's rls job keeps catching importers.
db_pkgs() {
  local mb d
  [ -n "$base" ] && mb=$(git merge-base "$base" HEAD 2> /dev/null) || return 0
  { git diff --name-only "$mb"; git ls-files -o --exclude-standard; } | grep '\.go$' |
    while read -r d; do dirname "$d"; done | sort -u |
    while read -r d; do
      if grep -qs DATABASE_ "$d"/*_test.go; then echo "./$d"; fi
    done | paste -sd ' ' -
}

suite fmt make fmt-check
suite go sh -c 'go build ./... && go vet ./... && go test ./...'
if [ -n "$base" ]; then
  suite gates sh -c 'rc=0
    go run ./internal/tools/citegate -base "$1" -head HEAD || rc=1
    go run ./internal/tools/stalerefs -base "$1" -head HEAD || rc=1
    exit $rc' sh "$base"
else
  echo "gates=skipped: no origin/main"
fi
if [ -n "${DEV_DB_PORT:-}" ]; then
  suite db sh -c 'rc=0
    make test-rls test-queue test-audit test-approvals DEV_DB_PORT="$1" || rc=1
    [ -z "$2" ] || make test-db PKGS="$2" DEV_DB_PORT="$1" || rc=1
    exit $rc' sh "$DEV_DB_PORT" "$(db_pkgs)"
else
  echo "db=skipped: no DEV_DB_PORT"
fi
suite spa sh -c 'pnpm -r typecheck && pnpm -r build'
# `pnpm -r test` alone would launch Playwright (e2e's `test` script is the browser suite).
# pnpm runs 4 packages at once and each vitest starts cores-1 workers: cap each at cores/4, min 1.
cores=$(getconf _NPROCESSORS_ONLN 2>/dev/null)
case $cores in ''|*[!0-9]*) workers=1 ;; *) workers=$(( cores / 4 )); [ "$workers" -ge 1 ] || workers=1 ;; esac
suite unit sh -c "rc=0; pnpm -r --no-bail --filter '!@invoice-os/e2e' test --maxWorkers=$workers || rc=1; pnpm --filter @invoice-os/e2e test:unit || rc=1; exit \$rc"

[ -z "$fs" ] || scripts/dev/suite-summary.sh "$L" $fs
exit "$failed"
