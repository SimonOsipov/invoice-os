#!/usr/bin/env bash
# Measures how many Go tests a change to internal/platform/db/tenant.go breaks.
#
# The request seam gates every HTTP-path read, so widening its predicate fails
# tests whose fixtures never modelled a membership. Counting them is the only
# honest way to size such a change.
#
#   scripts/dev/blast-radius.sh baseline            # stock tenant.go
#   scripts/dev/blast-radius.sh path/to/variant.go  # swap that file in first
#
# Needs the dev DB up: DEV_DB_PORT=5433 make dev-db. Restores tenant.go via trap.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TARGET="$REPO_ROOT/internal/platform/db/tenant.go"
PORT="${DEV_DB_PORT:-5432}"
OUT_DIR="${BLAST_OUT_DIR:-$REPO_ROOT/.ralph/blast-radius}"

VARIANT="${1:-baseline}"
mkdir -p "$OUT_DIR"
LOG="$OUT_DIR/${VARIANT##*/}.log"

BACKUP="$(mktemp)"
cp "$TARGET" "$BACKUP"
restore() { cp "$BACKUP" "$TARGET"; rm -f "$BACKUP"; }
trap restore EXIT

if [ "$VARIANT" != "baseline" ]; then
  [ -f "$VARIANT" ] || { echo "no such variant: $VARIANT" >&2; exit 2; }
  cp "$VARIANT" "$TARGET"
fi

echo "== variant: $VARIANT  (dev DB on :$PORT) =="
gofmt -l "$TARGET"

( cd "$REPO_ROOT" && \
  DATABASE_URL="postgres://invoice_app:app@localhost:$PORT/invoice_os?sslmode=disable" \
  DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:$PORT/invoice_os?sslmode=disable" \
  DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:$PORT/invoice_os?sslmode=disable" \
  DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:$PORT/invoice_os?sslmode=disable" \
  go test -count=1 -p 1 ./internal/... ) >"$LOG" 2>&1

# Top-level failures only: a --- FAIL line with no leading indent is a Test func,
# an indented one is a subtest and would double-count.
FAILED_TESTS=$(grep -c '^--- FAIL: ' "$LOG")
FAILED_PKGS=$(grep -c '^FAIL[[:space:]]\+github' "$LOG")

echo "failing tests:    $FAILED_TESTS"
echo "failing packages: $FAILED_PKGS"
echo "--- failing test names ---"
grep '^--- FAIL: ' "$LOG" | sed 's/^--- FAIL: /  /'
echo "--- failing packages ---"
grep '^FAIL[[:space:]]\+github' "$LOG" | awk '{print "  " $2}'
echo "--- build failures (if any) ---"
grep -n 'build failed\|\[build failed\]\|cannot use\|undefined:' "$LOG" | head -20
echo "(full log: $LOG)"
