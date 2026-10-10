#!/usr/bin/env sh
# railway-up-ci.sh <service>: `railway up --ci` with the failure classes a later gate can absorb.
# POSIX sh: runs in ghcr.io/railwayapp/cli (busybox ash, no bash/curl/jq). Tests: tools/prenv/railway_up_ci_test.go.
set -u

svc="${1:?usage: railway-up-ci.sh <service>}"
: "${RAILWAY_ENVIRONMENT:?RAILWAY_ENVIRONMENT is not set — expected the Railway environment name resolved by the prepare-env job in dev-env.yml (M4-23-03)}"
: "${RAILWAY_PROJECT_ID:?RAILWAY_PROJECT_ID is not set — expected the workflow-level constant from dev-env.yml}"

up() {
  rc=0
  out="$(railway up --ci --service "$svc" --environment "$RAILWAY_ENVIRONMENT" --project "$RAILWAY_PROJECT_ID" 2>&1)" || rc=$?
  printf '%s\n' "$out"
}

# A Build Logs URL means Railway accepted the deploy.
accepted() { case "$out" in *"Build Logs: https://"*) return 0 ;; esac; return 1; }

transport() {
  case "$out" in
    *"operation timed out"* | *"error sending request for url"* | *"Connection initialisation timeout"* | *"status code 5"[0-9][0-9]*) return 0 ;;
  esac
  return 1
}

build_failed() { case "$out" in *"Deploy failed"* | *"Build failed"*) return 0 ;; esac; return 1; }

stream_failed() { case "$out" in *"Failed to stream build logs"*) return 0 ;; esac; return 1; }

# A stream failure is tolerated below, so it never triggers a re-run.
rerun_blocked() {
  if build_failed || stream_failed; then return 0; fi
  case "$out" in *"Unauthorized"* | *"not found"*) return 0 ;; esac
  return 1
}

# The last matching line is the root cause of a "Caused by" chain.
sig_line() {
  printf '%s\n' "$out" | grep -E 'operation timed out|error sending request for url|Connection initialisation timeout|status code 5[0-9][0-9]' | tail -n 1 | sed 's/^[[:space:]]*//'
}

write_id() {
  [ -n "${GITHUB_OUTPUT:-}" ] || return 0
  id="$(printf '%s\n' "$out" | sed -n 's/.*Build Logs: https:[^ ]*[?&]id=\([^&[:space:]]*\).*/\1/p' | tail -n 1)"
  [ -n "$id" ] || return 0
  printf 'deployment_id_%s=%s\n' "$(printf '%s' "$svc" | sed 's/-/_/g')" "$id" >> "$GITHUB_OUTPUT"
}

# The later check that decides a build whose status poll was lost; empty = none.
case "$svc" in
  gateway) verdict="health-gate" ;;
  tenancy | portfolio | invoice | validation | submission | dashboard | notifications | reconciliation | docling) verdict="fleet-gate" ;;
  auth) verdict="fleet-gate's auth deployment wait" ;;
  landing | app | ops-console | support-console | library) verdict="The SPA build check" ;;
  *) verdict="" ;;
esac

up
first=""
if [ "$rc" -ne 0 ] && ! accepted && transport && ! rerun_blocked; then
  first="$(sig_line)"
  printf '::warning::railway up --service %s failed before Railway accepted the deploy (attempt 1/2): %s; re-running once.\n' "$svc" "$first"
  sleep 10
  up
fi

if [ "$rc" -eq 0 ]; then
  write_id
  exit 0
fi

if stream_failed; then
  printf '::warning::railway up --service %s exited %s on a build-log STREAM failure; the deploy was submitted. health-gate/fleet-gate verify actual health.\n' "$svc" "$rc"
  write_id
  exit 0
fi

if accepted && build_failed; then
  printf '::error::railway up --service %s: Railway accepted the deploy and the build failed (exit %s, see output above).\n' "$svc" "$rc"
  exit "$rc"
fi

if accepted && transport; then
  if [ -n "$verdict" ]; then
    printf '::warning::Railway accepted %s; the CLI lost its status poll: %s. %s decides.\n' "$svc" "$(sig_line)" "$verdict"
    write_id
    exit 0
  fi
  printf '::error::railway up --service %s: Railway accepted the deploy, the CLI lost its status poll (%s) and no later check covers %s'"'"'s build.\n' "$svc" "$(sig_line)" "$svc"
  exit "$rc"
fi

if [ -n "$first" ] && transport; then
  printf '::error::railway up --service %s failed twice before Railway accepted the deploy. Attempt 1: %s. Attempt 2: %s.\n' "$svc" "$first" "$(sig_line)"
  exit "$rc"
fi

printf '::error::railway up --service %s failed (exit %s) for a non-streaming reason (see output above).\n' "$svc" "$rc"
exit "$rc"
