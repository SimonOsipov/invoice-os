#!/usr/bin/env sh
# scripts/ci/railway-up-ci.sh <service>
#
# Wrap `railway up --ci` so a Railway build-log STREAMING failure does not fail the
# deploy step. `railway up --ci` exits non-zero on "Failed to stream build logs" even
# when the build + deploy SUCCEED (observed repeatedly 2026-07-13; Railway's status
# page showed no incident — a sub-threshold recurrence of the resolved 2026-05-27
# "Build Log Delivery Delays" class). `railway up --ci` returns after the BUILD, not
# after the deployment is healthy, so the REAL health oracle is already downstream:
# health-gate (gateway /healthz) + fleet-gate (/healthz/fleet, all 8 backends). Only
# the known stream-failure exit is tolerated; any OTHER non-zero exit (auth, unknown
# service, genuine build error) still fails the step.
#
# POSIX sh (not bash) on purpose: runs inside the minimal ghcr.io/railwayapp/cli
# container. Invoke as: sh scripts/ci/railway-up-ci.sh <service>
set -u

svc="${1:?usage: railway-up-ci.sh <service>}"

rc=0
out="$(railway up --ci --service "$svc" 2>&1)" || rc=$?
printf '%s\n' "$out"

if [ "$rc" -ne 0 ]; then
  case "$out" in
    *"Failed to stream build logs"*)
      printf '::warning::railway up --service %s exited %s on a build-log STREAM failure; the deploy was submitted. health-gate/fleet-gate verify actual health.\n' "$svc" "$rc"
      ;;
    *)
      printf '::error::railway up --service %s failed (exit %s) for a non-streaming reason (see output above).\n' "$svc" "$rc"
      exit "$rc"
      ;;
  esac
fi
