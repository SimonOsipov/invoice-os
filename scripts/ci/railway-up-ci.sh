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
# M4-21-09: every PR now deploys to its OWN Railway environment. An
# account-scoped RAILWAY_API_TOKEN (the new PR-path auth, M4-21-07/task-131)
# has no implicit project/environment scope the way the old project-scoped
# RAILWAY_API_DEV_TOKEN did — so this script now requires RAILWAY_ENVIRONMENT
# + RAILWAY_PROJECT_ID in its environment and passes them through as
# `--environment`/`--project` on every `railway up` call, for BOTH the PR
# path and the workflow_dispatch (development) path, so the target is always
# named explicitly rather than relying on whatever the token happens to be
# scoped to.
#
# POSIX sh (not bash) on purpose: runs inside the minimal ghcr.io/railwayapp/cli
# container. Invoke as: sh scripts/ci/railway-up-ci.sh <service>
set -u

svc="${1:?usage: railway-up-ci.sh <service>}"
: "${RAILWAY_ENVIRONMENT:?RAILWAY_ENVIRONMENT is not set — expected the Railway environment name resolved by dev-env.yml resolve-env job (M4-21-09)}"
: "${RAILWAY_PROJECT_ID:?RAILWAY_PROJECT_ID is not set — expected the workflow-level constant from dev-env.yml}"

rc=0
out="$(railway up --ci --service "$svc" --environment "$RAILWAY_ENVIRONMENT" --project "$RAILWAY_PROJECT_ID" 2>&1)" || rc=$?
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
