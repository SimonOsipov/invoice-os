#!/usr/bin/env bash
# Waits until no `go test` process or compiled test binary runs with its cwd inside DIR.
# Two suites in one worktree share its dev Postgres and fail each other with fake 28P01s.
# Usage: scripts/dev/wait-go-test-idle.sh <dir> [timeout-seconds, default 1800]
set -euo pipefail

dir="$(cd "${1:?usage: wait-go-test-idle.sh <dir> [timeout-seconds]}" && pwd -P)"
timeout="${2:-1800}"
waited=0

busy() {
  local pid cwd found=1
  # Anchored to argv[0]: a shell whose -c string merely mentions "go test" (e.g. our own caller) is not a suite.
  for pid in $(pgrep -f '^([^ ]*/)?go test|^[^ ]*\.test( |$)' || true); do
    [ "$pid" = "$$" ] && continue
    cwd="$(lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -1)"
    case "$cwd" in
      "$dir"|"$dir"/*) ps -o pid=,command= -p "$pid" 2>/dev/null; found=0 ;;
    esac
  done
  return "$found"
}

while busy; do
  if [ "$waited" -ge "$timeout" ]; then
    echo "go test still running under $dir after ${timeout}s; not starting another suite" >&2
    exit 1
  fi
  echo "waiting: go test running under $dir (${waited}s)"
  sleep 10
  waited=$((waited + 10))
done
echo "idle: no go test under $dir"
