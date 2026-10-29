#!/usr/bin/env bash
# Wrap one rls-job `go test` selection with the -json per-test-visibility + skip/zero-ran gate.
# Renders per-test PASS/SKIP/FAIL and fails the step if a DB-backed test SKIPPED (DB env broke),
# zero tests ran, or any test failed. Usage: scripts/ci/rls-test-gate.sh <go-test-args...>
# NIGHTLY_RESULTS set: retry via gotestretry, keep the stream in $NIGHTLY_RESULTS/gotest-*.json.
set -uo pipefail
results="$(mktemp)"
trap 'rm -f "$results"' EXIT
if [ -n "${NIGHTLY_RESULTS:-}" ]; then
  go run ./internal/tools/gotestretry -out "$results" -- "$@"
  gotest_rc=$?
  if keep="$(mkdir -p "$NIGHTLY_RESULTS" && mktemp "$NIGHTLY_RESULTS/gotest-XXXXXX")" &&
    mv "$keep" "$keep.json" && cp "$results" "$keep.json"; then :; else
    echo "::warning::could not keep the go test stream in $NIGHTLY_RESULTS"
  fi
else
  go test -json "$@" > "$results"
  gotest_rc=$?
fi
go run ./internal/tools/rlsgate < "$results"
gate_rc=$?
if [ "$gotest_rc" -ne 0 ]; then
  echo "::error::go test exited $gotest_rc (compile error / panic / failure) for: $*"
  exit 1
fi
exit "$gate_rc"
