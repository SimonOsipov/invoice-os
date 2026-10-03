#!/usr/bin/env bash
# Runs Ralph's local suites in this worktree, one log per suite in .ralph/.
# Ralph runs it through `hm suite run`, which runs one worktree's suites at a time on the machine.
# Usage: [DEV_DB_PORT=<port>] scripts/dev/suites.sh   (no DEV_DB_PORT: the DB suites are skipped)
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1
L=.ralph
mkdir -p "$L"

if ! scripts/dev/wait-go-test-idle.sh . > "$L/wait.log" 2>&1; then
  echo "wait=1: a go test still runs in this worktree; no suite ran"
  exit 1
fi

failed=0
suite() { # suite <name> <command...>
  local name=$1 rc
  shift
  "$@" > "$L/$name.log" 2>&1
  rc=$?
  echo "$name=$rc"
  [ "$rc" -eq 0 ] || failed=1
}

suite fmt make fmt-check
suite go sh -c 'go build ./... && go vet ./... && go test ./...'
if [ -n "${DEV_DB_PORT:-}" ]; then
  suite db make test-rls test-queue test-audit DEV_DB_PORT="$DEV_DB_PORT"
else
  echo "db=skipped: no DEV_DB_PORT"
fi
suite spa sh -c 'pnpm -r typecheck && pnpm -r build'
# `pnpm -r test` alone would launch Playwright (e2e's `test` script is the browser suite).
suite unit sh -c "pnpm -r --filter '!@invoice-os/e2e' test && pnpm --filter @invoice-os/e2e test:unit"

grep -hE '^(ok|FAIL|--- FAIL|Test Files|Tests)' "$L"/*.log | tail -40
exit "$failed"
