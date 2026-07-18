#!/usr/bin/env bash
# scripts/ci/railway-env.sh <assert-project-settings|disable-pr-environments|
#                            ensure-environment <name>|audit-sealed-variables|
#                            reconcile-fork <environment-id>|
#                            delete-environment <name>>
#
# M4-23-02: Railway's PR Environments must stay OFF for this project.
#
# `assert-project-settings` ASSERTS and FAILS LOUDLY. It never repairs: a silent
# auto-repair would erase the evidence that someone re-enabled the feature, which is
# exactly the "half-works and misleads the next person" failure this check exists to
# prevent.
#
# It asserts at TWO levels, because level 1 alone proves nothing here:
#   1. prDeploys == false AND botPrEnvironments == false — the literal invariant.
#   2. ZERO deploymentTriggers across every environment — the condition that actually
#      made Railway's PR Environments inert in this project. M4-23 established that
#      prDeploys was `true`, correctly configured, and completely inert, because Railway
#      is not subscribed to this repo's PR events (the fleet deploys by CLI push). A
#      green check on (1) alone asserts a suggestively-named field with no causal power.
#      A deployment trigger, by contrast, lets Railway deploy behind CI's back in its own
#      order — breaking the gateway-first sequencing that dev-env.yml depends on.
#
# Uses the nested, NON-deprecated project.environments[].deploymentTriggers.
# `Project.deploymentTriggers` is @deprecated ("Use environment.deploymentTriggers for
# properly scoped access control"); a deprecated field can be removed, which would turn
# this check into a false positive that blocks every PR.
#
# `disable-pr-environments` is the repair path, reachable ONLY via
# `gh workflow run railway-invariants.yml -f disable=true` — never on the pull_request
# path. After mutating it re-verifies with an INDEPENDENT read-back request rather than
# echoing projectUpdate's own selection set; echoing the mutation response back is the
# silent-no-op defect this exists to close.
#
# `ensure-environment <name>` (M4-23-03) is the create-or-reuse path that replaced the
# ~300s poll for an environment Railway was supposed to create for us. Idempotent: it
# looks the name up first and mutates nothing when it already exists.
#
# `delete-environment <name>` (M4-23-05) is the teardown path, called by
# dev-env-teardown.yml on PR close. It takes a NAME and never an id, so the
# never-delete-`development` guard cannot be bypassed by the caller, and it proves the
# delete by an independent re-query rather than by environmentDelete's bare Boolean.
#
# `audit-sealed-variables` and `reconcile-fork <environment-id>` (M4-23-04) close the
# fork-fidelity gaps. See the M4-23-04 banner further down: two checks the design asked
# for are deliberately absent because a live probe proved they would fail every run.
#
# Auth: account-scoped RAILWAY_API_TOKEN, `Authorization: Bearer`. A Railway *project*
# token is pinned to one environment and cannot perform projectUpdate, nor reach an
# ephemeral PR environment.
#
# bash, not POSIX sh: unlike scripts/ci/railway-up-ci.sh this does NOT run inside the
# minimal ghcr.io/railwayapp/cli container.
set -euo pipefail

RAILWAY_GRAPHQL_URL="https://backboard.railway.com/graphql/v2"

# All three booleans are non-null in Railway's schema (Boolean!), so `false` here is a
# positive observation and can never be an absent field misread as off. baseEnvironmentId
# is nullable (String) → reported, never asserted. focusedPrEnvironments is reported but
# NOT asserted: with prDeploys=false it is inert, so asserting it adds no protection.
# shellcheck disable=SC2016  # $id is a GraphQL variable — it must NOT be shell-expanded.
READBACK_QUERY='query prEnvSettings($id: String!) {
  project(id: $id) {
    id
    name
    prDeploys
    botPrEnvironments
    focusedPrEnvironments
    baseEnvironmentId
    environments {
      edges { node {
        id
        name
        deploymentTriggers { edges { node { id provider repository branch environmentId serviceId } } }
      } }
    }
  }
}'

# shellcheck disable=SC2016  # $id/$input are GraphQL variables — not shell expansions.
DISABLE_MUTATION='mutation disablePrEnvironments($id: String!, $input: ProjectUpdateInput!) {
  projectUpdate(id: $id, input: $input) { id name prDeploys botPrEnvironments focusedPrEnvironments }
}'

# M4-23-03. DELIBERATELY UNFILTERED — `isEphemeral` is selected as a DISCRIMINATOR, not
# passed as a server-side filter. With `environments(projectId:$p, isEphemeral:true)` a
# PERSISTENT environment that happens to hold our name is invisible, so we would try to
# create over it on every run, forever. Unfiltered + a client-side name match lets arity
# decide (see lookup_environment).
# shellcheck disable=SC2016  # $p is a GraphQL variable — it must NOT be shell-expanded.
ENV_LIST_QUERY='query envList($p: String!) {
  environments(projectId: $p) {
    edges { node { id name isEphemeral } }
  }
}'

# `stageInitialChanges` is OMITTED (defaults false). true is the dashboard "Duplicate
# Environment" behaviour, which stages every service and needs an explicit
# environmentPatchCommitStaged before anything deploys. false commits immediately;
# `skipInitialDeploys` suppresses only the deployments.
# Read/write asymmetry, easy bug: the WRITE field is `ephemeral`, the READ field is
# `isEphemeral`.
# shellcheck disable=SC2016  # $input is a GraphQL variable — not a shell expansion.
ENV_CREATE_MUTATION='mutation createPrEnvironment($input: EnvironmentCreateInput!) {
  environmentCreate(input: $input) { id name isEphemeral }
}'

# M4-23-05. environmentDelete returns a BARE Boolean, the same shape this file already
# distrusts for serviceInstanceRedeploy: `true` is indistinguishable from a silent no-op.
# So the return value is NEVER the proof of a delete — an independent re-query is.
# shellcheck disable=SC2016  # $id is a GraphQL variable — it must NOT be shell-expanded.
ENV_DELETE_MUTATION='mutation deletePrEnvironment($id: String!) {
  environmentDelete(id: $id)
}'

# Response of the most recent successful GraphQL call.
GQL_RESPONSE=""
# Failure text of the most recent graphql_try that returned non-zero.
GQL_ERROR=""
# Environment id found by the most recent successful lookup_environment.
LOOKUP_ID=""

require_env() {
  if [ -z "${RAILWAY_API_TOKEN:-}" ]; then
    echo "::error::RAILWAY_API_TOKEN is not set. This needs the ACCOUNT-scoped token (Account Settings -> Tokens); a project token cannot read or change project-level settings. Note: a fork PR receives no secrets and will fail here by design."
    exit 1
  fi
  if [ -z "${RAILWAY_PROJECT_ID:-}" ]; then
    echo "::error::RAILWAY_PROJECT_ID is not set — expected the workflow-level constant."
    exit 1
  fi
}

# graphql_post <json-body> <context-label>
# Sets GQL_RESPONSE. Exits non-zero on transport failure OR on any `.errors` payload —
# a GraphQL error (including "Not Authorized") is NEVER interpreted as "already off".
graphql_post() {
  local body="$1" ctx="$2"

  if ! GQL_RESPONSE=$(curl -fsS --connect-timeout 5 --max-time 15 \
        --request POST \
        --url "$RAILWAY_GRAPHQL_URL" \
        --header "Authorization: Bearer $RAILWAY_API_TOKEN" \
        --header "Content-Type: application/json" \
        --data "$body"); then
    echo "::error::Railway GraphQL request failed while $ctx"
    exit 1
  fi

  if echo "$GQL_RESPONSE" | jq -e '.errors' >/dev/null 2>&1; then
    echo "::error::Railway GraphQL error while $ctx: $(echo "$GQL_RESPONSE" | jq -c '.errors')"
    exit 1
  fi
}

# graphql_try <json-body> <context-label>
# Same request as graphql_post, but RETURNS 1 (leaving the reason in GQL_ERROR) instead
# of exiting. The create path must be able to survive its own failure in order to
# re-query and adopt, which graphql_post's exit-on-error makes impossible.
graphql_try() {
  local body="$1" ctx="$2"
  GQL_ERROR=""

  if ! GQL_RESPONSE=$(curl -fsS --connect-timeout 5 --max-time 30 \
        --request POST \
        --url "$RAILWAY_GRAPHQL_URL" \
        --header "Authorization: Bearer $RAILWAY_API_TOKEN" \
        --header "Content-Type: application/json" \
        --data "$body"); then
    GQL_ERROR="Railway GraphQL request failed (transport) while $ctx"
    return 1
  fi

  if echo "$GQL_RESPONSE" | jq -e '.errors' >/dev/null 2>&1; then
    GQL_ERROR="Railway GraphQL error while $ctx: $(echo "$GQL_RESPONSE" | jq -c '.errors')"
    return 1
  fi
  return 0
}

# Issues the read-back query as its own request and leaves it in GQL_RESPONSE.
read_project_settings() {
  local body
  body=$(jq -n --arg q "$READBACK_QUERY" --arg id "$RAILWAY_PROJECT_ID" \
    '{query: $q, variables: {id: $id}}')

  graphql_post "$body" "reading project settings for project $RAILWAY_PROJECT_ID"

  if echo "$GQL_RESPONSE" | jq -e '.data.project == null' >/dev/null 2>&1; then
    echo "::error::Railway returned a null project for id $RAILWAY_PROJECT_ID — the project was deleted or the token lost access to it. This is NOT evidence that PR Environments are off."
    exit 1
  fi
}

# Asserts the invariant against whatever read_project_settings last observed.
assert_pr_environments_off() {
  local pr_deploys bot_pr focused base_env trigger_count offenders

  pr_deploys=$(echo "$GQL_RESPONSE" | jq -r '.data.project.prDeploys')
  bot_pr=$(echo "$GQL_RESPONSE" | jq -r '.data.project.botPrEnvironments')
  focused=$(echo "$GQL_RESPONSE" | jq -r '.data.project.focusedPrEnvironments')
  base_env=$(echo "$GQL_RESPONSE" | jq -r '.data.project.baseEnvironmentId // "null"')

  # Compared against the literal string "false", so anything else — true, null, a missing
  # field — fails. Exiting 0 requires positively observing `false`.
  if [ "$pr_deploys" != "false" ] || [ "$bot_pr" != "false" ]; then
    echo "::error::Railway PR Environments are not OFF (prDeploys=$pr_deploys botPrEnvironments=$bot_pr). This check does NOT repair. Turn them off with: gh workflow run railway-invariants.yml -f disable=true"
    exit 1
  fi

  trigger_count=$(echo "$GQL_RESPONSE" | jq '[.data.project.environments.edges[]?.node.deploymentTriggers.edges[]?] | length')
  if [ "$trigger_count" != "0" ]; then
    offenders=$(echo "$GQL_RESPONSE" | jq -r '
      .data.project.environments.edges[]?.node
      | . as $env
      | $env.deploymentTriggers.edges[]?.node
      | "  environment=\($env.name) (\($env.id)) provider=\(.provider) repository=\(.repository) branch=\(.branch) serviceId=\(.serviceId) triggerId=\(.id)"
    ')
    echo "::error::$trigger_count Railway deployment trigger(s) found. Railway can now deploy this project on its own schedule, outside CI, breaking the gateway-first deploy ordering dev-env.yml depends on. Offenders:"
    echo "$offenders"
    exit 1
  fi

  echo "Railway PR Environments OFF: prDeploys=$pr_deploys botPrEnvironments=$bot_pr focusedPrEnvironments=$focused baseEnvironmentId=$base_env deploymentTriggers=$trigger_count"
}

require_source_env() {
  if [ -z "${RAILWAY_DEV_ENVIRONMENT_ID:-}" ]; then
    echo "::error::RAILWAY_DEV_ENVIRONMENT_ID is not set — expected the workflow-level constant from dev-env.yml. It is passed EXPLICITLY as environmentCreate's sourceEnvironmentId and there is NO fallback: this project's baseEnvironmentId was measured null (2026-07-18), so an omitted source does not quietly default to \`development\` — it forks from nothing and yields an EMPTY environment (or a hard API error)."
    exit 1
  fi
}

# lookup_environment <name>
# Sets LOOKUP_ID and returns 0 when exactly one EPHEMERAL environment carries the name.
# Returns 1 when the name is definitively absent (0 matches) — the only condition that
# may lead to a create.
#
# A transport/GraphQL failure is NEVER reported as "absent": that conflation would let a
# blip trigger a create and duplicate an environment that already exists. It is retried a
# bounded number of times and then exits loudly. Arity violations are not transient and
# exit immediately.
lookup_environment() {
  local name="$1" body count ephemeral try
  body=$(jq -n --arg q "$ENV_LIST_QUERY" --arg p "$RAILWAY_PROJECT_ID" \
    '{query: $q, variables: {p: $p}}')

  for try in 1 2 3; do
    if graphql_try "$body" "listing environments in project $RAILWAY_PROJECT_ID"; then
      break
    fi
    if [ "$try" = "3" ]; then
      echo "::error::Could not list environments in project $RAILWAY_PROJECT_ID after 3 attempts: $GQL_ERROR. Refusing to treat an unreadable environment list as 'the environment does not exist' — that would create a duplicate."
      exit 1
    fi
    echo "  (lookup attempt $try) $GQL_ERROR — retrying in 5s ..."
    sleep 5
  done

  count=$(echo "$GQL_RESPONSE" | jq --arg n "$name" \
    '[.data.environments.edges[]?.node | select(.name == $n)] | length')

  if [ "$count" = "0" ]; then
    LOOKUP_ID=""
    return 1
  fi

  if [ "$count" != "1" ]; then
    echo "::error::$count environments in project $RAILWAY_PROJECT_ID are named '$name'. Refusing to guess which one is this PR's — that ambiguity must be resolved by hand. Matches:"
    echo "$GQL_RESPONSE" | jq -r --arg n "$name" \
      '.data.environments.edges[]?.node | select(.name == $n) | "  id=\(.id) isEphemeral=\(.isEphemeral)"'
    exit 1
  fi

  ephemeral=$(echo "$GQL_RESPONSE" | jq -r --arg n "$name" \
    '.data.environments.edges[]?.node | select(.name == $n) | .isEphemeral')
  LOOKUP_ID=$(echo "$GQL_RESPONSE" | jq -r --arg n "$name" \
    '.data.environments.edges[]?.node | select(.name == $n) | .id')

  if [ "$ephemeral" != "true" ]; then
    echo "::error::An environment named '$name' exists but is NOT ephemeral (isEphemeral=$ephemeral, id=$LOOKUP_ID). Refusing to create over it or deploy into it: both the teardown workflow and the orphan sweeper filter on isEphemeral, so a persistent environment wearing a PR name would never be cleaned up and would leak forever. Rename or delete it by hand."
    exit 1
  fi

  return 0
}

# attempt_create <name>
# One environmentCreate. Returns non-zero (reason in GQL_ERROR) instead of exiting, so the
# caller can re-query and adopt.
attempt_create() {
  local name="$1" body
  body=$(jq -n --arg q "$ENV_CREATE_MUTATION" --arg n "$name" \
    --arg p "$RAILWAY_PROJECT_ID" --arg s "$RAILWAY_DEV_ENVIRONMENT_ID" \
    '{query: $q, variables: {input: {
        name: $n,
        projectId: $p,
        sourceEnvironmentId: $s,
        ephemeral: true,
        skipInitialDeploys: true
      }}}')

  graphql_try "$body" "creating ephemeral environment '$name' as a fork of $RAILWAY_DEV_ENVIRONMENT_ID"
}

emit_environment_outputs() {
  local name="$1" id="$2"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    {
      echo "environment=$name"
      echo "environment_id=$id"
    } >> "$GITHUB_OUTPUT"
  fi
  echo "environment=$name environment_id=$id"
}

# cmd_ensure_environment <name>
# Idempotent create-or-reuse. Reuse costs ZERO mutations. Creation is bounded at TWO
# attempts, and every attempt — successful or not — is followed by an INDEPENDENT
# re-query, never by a blind retry. Railway can return 504 having created the environment
# anyway, so a create that reports failure is not evidence that nothing was created; only
# the re-query is. This is the same discipline cmd_disable_pr_environments applies when it
# refuses to trust projectUpdate's own selection set.
cmd_ensure_environment() {
  local name="${1:-}"
  if [ -z "$name" ]; then
    echo "::error::usage: railway-env.sh ensure-environment <name>"
    exit 2
  fi
  require_env
  require_source_env

  echo "Looking for an environment named '$name' in project $RAILWAY_PROJECT_ID ..."
  if lookup_environment "$name"; then
    echo "Reusing the existing ephemeral environment '$name' ($LOOKUP_ID) — no mutation performed."
    emit_environment_outputs "$name" "$LOOKUP_ID"
    return 0
  fi

  echo "No environment named '$name' exists — creating it as a fork of $RAILWAY_DEV_ENVIRONMENT_ID ..."

  local err1="" err2="" why attempt created_ok=0
  for attempt in 1 2; do
    why=""
    if [ "$attempt" = "2" ] && [ "$created_ok" = "1" ]; then
      # Attempt 1's environmentCreate REPORTED SUCCESS and the confirming
      # re-query still could not see it. Do NOT create again: read-after-write
      # lag is far likelier than a phantom success, and a second create would
      # fork a DUPLICATE pr-<N> — which lookup_environment would then refuse as
      # ambiguous on every subsequent run, leaving two environments to clean up
      # by hand. Wait longer and re-query instead.
      echo "The environment is still not visible after a create that reported success — waiting again and re-querying. Deliberately NOT creating a second time: that would fork a duplicate."
      sleep 15
    elif attempt_create "$name"; then
      created_ok=1
      echo "environmentCreate (attempt $attempt) returned without error; confirming with an INDEPENDENT re-query rather than trusting the mutation's own selection set ..."
      # Railway is read-after-write lagged often enough that an instant re-query
      # is the worst possible moment to ask.
      sleep 10
    else
      created_ok=0
      why="$GQL_ERROR"
      echo "::warning::environmentCreate attempt $attempt failed: $why"
      echo "Railway can report failure having created the environment anyway — waiting 15s and re-querying instead of blindly retrying."
      sleep 15
    fi

    if lookup_environment "$name"; then
      if [ -n "$why" ]; then
        echo "Adopted '$name' ($LOOKUP_ID) after a create that reported failure (Railway 504-still-creates)."
      else
        echo "Created ephemeral environment '$name' ($LOOKUP_ID)."
      fi
      emit_environment_outputs "$name" "$LOOKUP_ID"
      return 0
    fi

    if [ -z "$why" ]; then
      why="environmentCreate (attempt $attempt) reported success but an independent re-query found no environment named '$name'"
    fi
    if [ "$attempt" = "1" ]; then err1="$why"; else err2="$why"; fi
  done

  echo "::error::Failed to create or adopt the ephemeral environment '$name' after 2 rounds, each followed by an independent re-query."
  echo "  attempt 1: $err1"
  echo "  attempt 2: $err2"
  exit 1
}

# cmd_delete_environment <name>
# M4-23-05. Deletes one PR's ephemeral environment on PR close.
#
# Takes a NAME, never an id. That is deliberate and load-bearing: an id argument would let
# a caller pass `development`'s id straight through and bypass the isEphemeral guard
# entirely. Name-only makes the guard non-bypassable from the workflow.
#
# Exit-code contract — pinned, and NOT to be blurred into "best-effort":
#   absent                              -> 0  (nothing to delete IS the desired end state)
#   deleted + confirmed by re-query     -> 0
#   present, delete not confirmed       -> 1  (a delete that demonstrably did not happen)
#   non-ephemeral / ambiguous / API down-> 1  (fail loud, never guess)
# "Best-effort" ([teardown-best-effort-sweeper-authoritative]) describes the TRIGGER not
# firing. It does not license swallowing a failed delete: a red teardown run is the only
# notification channel there is, so do NOT add continue-on-error to the workflow.
cmd_delete_environment() {
  local name="${1:-}"
  if [ -z "$name" ]; then
    echo "::error::usage: railway-env.sh delete-environment <name>"
    exit 2
  fi
  require_env
  # NOT require_source_env — teardown forks nothing, so it has no source environment.

  echo "Looking for an ephemeral environment named '$name' in project $RAILWAY_PROJECT_ID ..."

  # lookup_environment supplies every safety property this command needs, so it is reused
  # verbatim rather than reimplemented: it exits 1 on >1 match (refusing to guess which
  # environment to DELETE), exits 1 on a non-ephemeral match (the primary never-delete-
  # `development` guard, whose error text at :268 already names this caller), and — most
  # importantly — NEVER reports a transport or GraphQL failure as "absent". That last
  # property is what stops an API blip being logged as a successful teardown.
  if ! lookup_environment "$name"; then
    echo "No environment named '$name' exists in project $RAILWAY_PROJECT_ID — nothing to delete. This is the desired end state: the PR was never deployed, or the sweeper reaped it first."
    return 0
  fi

  # Second, independent guard, mirroring cmd_reconcile_fork:925-928. The isEphemeral filter
  # above is the primary one; this catches the residual cases it structurally cannot —
  # Railway ever mislabelling `development` as ephemeral, or a human renaming `development`
  # to `pr-<N>`.
  if [ "$LOOKUP_ID" = "$RAILWAY_DEV_ENVIRONMENT_ID" ]; then
    echo "::error::Refusing to delete the persistent development environment ($LOOKUP_ID): an environment named '$name' resolved to the id this workflow knows as RAILWAY_DEV_ENVIRONMENT_ID. The development environment is persistent ([dev-env-status]) and no teardown may ever remove it. Investigate by hand — either Railway is reporting it as ephemeral, or it has been renamed."
    exit 1
  fi

  # Captured up front and used everywhere below. lookup_environment CLEARS LOOKUP_ID when
  # it reports absent, so the confirming re-query destroys it moments before the success
  # line is logged — reading LOOKUP_ID after that point silently prints an empty id and
  # throws away the one forensic detail a teardown log needs.
  local env_id="$LOOKUP_ID"

  echo "Deleting ephemeral environment '$name' ($env_id) ..."

  local body err1="" err2="" why
  body=$(gql_body "$ENV_DELETE_MUTATION" "$(jq -n --arg id "$env_id" '{id: $id}')")

  # Two rounds, each mutate-then-INDEPENDENTLY-re-query. Absent after the mutation means
  # success EVEN IF the mutation itself reported an error — the same adopt-after-apparent-
  # failure discipline attempt_create applies, and it also correctly absorbs the sweeper
  # racing us to the very same delete.
  local round wait
  for round in 1 2; do
    why=""
    if ! graphql_try "$body" "deleting environment '$name' ($env_id)"; then
      why="$GQL_ERROR"
      echo "::warning::environmentDelete (round $round) failed: $why"
      echo "Railway can report failure having deleted the environment anyway — re-querying instead of trusting the mutation's verdict."
    else
      echo "environmentDelete (round $round) returned without error; confirming with an INDEPENDENT re-query rather than trusting a bare Boolean ..."
    fi

    if [ "$round" = "1" ]; then wait=5; else wait=10; fi
    sleep "$wait"

    if ! lookup_environment "$name"; then
      if [ -n "$why" ]; then
        echo "Environment '$name' is gone, confirmed by an independent re-query, after a delete that reported failure."
      else
        echo "Deleted environment '$name' ($env_id), confirmed by an independent re-query."
      fi
      return 0
    fi

    if [ -z "$why" ]; then
      why="environmentDelete (round $round) reported success but an independent re-query still finds environment '$name' ($env_id)"
    fi
    if [ "$round" = "1" ]; then err1="$why"; else err2="$why"; fi
  done

  echo "::error::Failed to delete the ephemeral environment '$name' ($env_id) after 2 rounds, each followed by an independent re-query. The environment is still present, so this is a real failure and not a no-op — the orphan sweeper (M4-23-07) will reap it, but investigate why teardown could not."
  echo "  round 1: $err1"
  echo "  round 2: $err2"
  exit 1
}

cmd_assert_project_settings() {
  require_env
  read_project_settings
  assert_pr_environments_off
}

cmd_disable_pr_environments() {
  require_env

  local body
  body=$(jq -n --arg q "$DISABLE_MUTATION" --arg id "$RAILWAY_PROJECT_ID" \
    '{query: $q, variables: {id: $id, input: {prDeploys: false, botPrEnvironments: false}}}')

  echo "Disabling Railway PR Environments on project $RAILWAY_PROJECT_ID ..."
  graphql_post "$body" "running projectUpdate on project $RAILWAY_PROJECT_ID"

  # Deliberately NOT trusting projectUpdate's own selection set. Re-read from scratch.
  echo "projectUpdate returned without error; re-reading project settings independently ..."
  read_project_settings

  local pr_deploys bot_pr
  pr_deploys=$(echo "$GQL_RESPONSE" | jq -r '.data.project.prDeploys')
  bot_pr=$(echo "$GQL_RESPONSE" | jq -r '.data.project.botPrEnvironments')
  if [ "$pr_deploys" != "false" ] || [ "$bot_pr" != "false" ]; then
    echo "::error::projectUpdate reported success but the independent read-back disagrees (prDeploys=$pr_deploys botPrEnvironments=$bot_pr) — the setting did NOT take."
    exit 1
  fi
  echo "Independent read-back confirms prDeploys=false botPrEnvironments=false."
}

# ===========================================================================
# M4-23-04 — fork fidelity.
#
# EVERY behaviour below was MEASURED against a real probe fork on 2026-07-18
# (forked from `development` with the exact payload attempt_create sends), not
# inferred from the docs. Where a measurement contradicted the design, the
# measurement won. Two checks the design called for are DELIBERATELY ABSENT
# because measuring them showed they would have failed every healthy run:
#
#   1. NO failure on a null `targetPort`. Measured null on the gateway domain in
#      BOTH the fork AND `development`. Null is the normal state here.
#   2. NO failure on an absent volume. A fork gets NO volume at all
#      (volumeInstances == []) while `development` has a 5000MB one — and
#      Postgres still deploys, reaches SUCCESS and accepts TCP connections. A PR
#      database is EPHEMERAL BY DESIGN and that is correct: the gateway
#      bootstraps, migrates and seeds at boot, so it is meant to be born empty.
#
# Do not "restore" either check. Each would have converted every green run red.
# ===========================================================================

# Domains and the TCP proxy were both measured to CARRY into a fork (auto-renamed
# `<svc>-pr-<N>.up.railway.app`, and a proxy with its own distinct port). The
# create path below is INSURANCE, not the expected path: the normal run is one
# query per service and zero mutations.
# shellcheck disable=SC2016  # $p/$e/$s are GraphQL variables — not shell expansions.
DOMAINS_QUERY='query dom($p: String!, $e: String!, $s: String!) {
  domains(projectId: $p, environmentId: $e, serviceId: $s) {
    serviceDomains { id domain targetPort syncStatus }
  }
}'

# shellcheck disable=SC2016  # $input is a GraphQL variable — not a shell expansion.
DOMAIN_CREATE_MUTATION='mutation domCreate($input: ServiceDomainCreateInput!) {
  serviceDomainCreate(input: $input) { id domain targetPort }
}'

# shellcheck disable=SC2016  # $e is a GraphQL variable — not a shell expansion.
SEALED_AUDIT_QUERY='query sealedAudit($e: String!) {
  environment(id: $e) {
    id
    name
    variables(first: 500) { edges { node { name isSealed serviceId } } }
  }
}'

# shellcheck disable=SC2016  # $e is a GraphQL variable — not a shell expansion.
SETTLE_QUERY='query settle($e: String!) {
  environment(id: $e) { serviceInstances { edges { node { serviceId serviceName } } } }
}'

# shellcheck disable=SC2016  # $e/$s are GraphQL variables — not shell expansions.
SERVICE_INSTANCE_QUERY='query svcInstance($e: String!, $s: String!) {
  serviceInstance(environmentId: $e, serviceId: $s) {
    serviceName
    latestDeployment { id status createdAt }
  }
}'

# `commitSha` is optional (verified by introspection) and is OMITTED: Postgres is
# image-sourced, so there is no commit to pin. V2 is the create-a-deployment verb
# and returns a deployment ID; `serviceInstanceRedeploy` returns a bare Boolean,
# which is indistinguishable from a silent no-op when there is nothing to redeploy.
# shellcheck disable=SC2016  # $e/$s are GraphQL variables — not shell expansions.
SERVICE_DEPLOY_MUTATION='mutation svcDeploy($e: String!, $s: String!) {
  serviceInstanceDeployV2(environmentId: $e, serviceId: $s)
}'

# shellcheck disable=SC2016  # $e/$s are GraphQL variables — not shell expansions.
SERVICE_REDEPLOY_MUTATION='mutation svcRedeploy($e: String!, $s: String!) {
  serviceInstanceRedeploy(environmentId: $e, serviceId: $s)
}'

# shellcheck disable=SC2016  # $id is a GraphQL variable — not a shell expansion.
DEPLOYMENT_STATUS_QUERY='query dep($id: String!) { deployment(id: $id) { id status } }'

# shellcheck disable=SC2016  # $e is a GraphQL variable — not a shell expansion.
VOLUMES_QUERY='query vols($e: String!) {
  environment(id: $e) {
    volumeInstances { edges { node { id serviceId mountPath sizeMB currentSizeMB state } } }
  }
}'

# shellcheck disable=SC2016  # $e/$s are GraphQL variables — not shell expansions.
TCP_PROXIES_QUERY='query proxies($e: String!, $s: String!) {
  tcpProxies(environmentId: $e, serviceId: $s) { id domain proxyPort applicationPort syncStatus }
}'

# Returns the RENDERED variable map. Never logged wholesale — only the two named
# keys are read out of it, because the same map carries live credentials.
# shellcheck disable=SC2016  # $p/$e/$s are GraphQL variables — not shell expansions.
SERVICE_VARIABLES_QUERY='query svcVars($p: String!, $e: String!, $s: String!) {
  variables(projectId: $p, environmentId: $e, serviceId: $s)
}'

# Attempt counts, not wall-clock deadlines: a count remains bounded when `sleep` is
# stubbed out under test, whereas a $SECONDS deadline would spin forever.
SETTLE_ATTEMPTS=6          # x10s = 60s. Insurance only — see settle_fork.
SETTLE_INTERVAL=10
PG_WAIT_ATTEMPTS=42        # x10s = 420s.
PG_WAIT_INTERVAL=10

# gql_body <query> <variables-json>
gql_body() {
  jq -n --arg q "$1" --argjson v "$2" '{query: $q, variables: $v}'
}

require_fork_ids() {
  local v missing=""
  for v in RAILWAY_SVC_GATEWAY_ID RAILWAY_SVC_APP_ID RAILWAY_SVC_LANDING_ID \
           RAILWAY_SVC_OPS_CONSOLE_ID RAILWAY_SVC_POSTGRES_ID; do
    if [ -z "${!v:-}" ]; then missing="$missing $v"; fi
  done
  if [ -n "$missing" ]; then
    echo "::error::Missing Railway service id(s):$missing — expected the workflow-level constants in dev-env.yml's env: block."
    exit 1
  fi
}

# --- Reconcile A: sealed variables in `development` -------------------------
#
# Sealed variables do NOT fork. A fork would be created with them silently
# missing, surfacing much later as an unexplained boot or auth error in a PR
# environment that looks correctly configured. Assert, never repair — a sealed
# value is unreadable by definition, so there is nothing to copy.
cmd_audit_sealed_variables() {
  require_env
  require_source_env

  local body total count offenders
  body=$(gql_body "$SEALED_AUDIT_QUERY" "$(jq -n --arg e "$RAILWAY_DEV_ENVIRONMENT_ID" '{e: $e}')")
  graphql_post "$body" "auditing sealed variables in the source environment $RAILWAY_DEV_ENVIRONMENT_ID"

  # "Cannot audit" is NEVER "none present" — the same discipline
  # assert_pr_environments_off applies to a null project.
  if echo "$GQL_RESPONSE" | jq -e '.data.environment == null' >/dev/null 2>&1; then
    echo "::error::Railway returned a null environment for the source id $RAILWAY_DEV_ENVIRONMENT_ID, so the sealed-variable audit could not run. This is NOT evidence that no sealed variables exist."
    exit 1
  fi

  total=$(echo "$GQL_RESPONSE" | jq '[.data.environment.variables.edges[]?.node] | length')
  count=$(echo "$GQL_RESPONSE" | jq '[.data.environment.variables.edges[]?.node | select(.isSealed == true)] | length')

  if [ "$count" != "0" ]; then
    offenders=$(echo "$GQL_RESPONSE" | jq -r '
      .data.environment.variables.edges[]?.node
      | select(.isSealed == true)
      | "  \(.name) (serviceId=\(.serviceId // "environment-scoped"))"')
    echo "::error::$count sealed variable(s) found in the source environment $RAILWAY_DEV_ENVIRONMENT_ID. Sealed variables do NOT fork: every pr-<N> environment would be created with these SILENTLY MISSING, surfacing much later as an unexplained boot or auth failure. Unseal them or move them to a non-sealed variable. Offenders:"
    echo "$offenders"
    exit 1
  fi

  echo "Sealed-variable audit clean: 0 of $total variables in the source environment are sealed."
}

# --- Reconcile B: settle -----------------------------------------------------
#
# MEASURED: all 12 service instances materialise IMMEDIATELY after
# environmentCreate. There is NO settle race. This poll is kept as cheap
# insurance against read-after-write lag only, and is deliberately SHORT (60s)
# because it is not guarding a race that was ever observed. Do not cite a race
# as its justification.
#
# It checks only the 5 service ids this command goes on to act on. It is NOT a
# second copy of the Watch-Paths assertion's 11-service list, which remains the
# sole authority on fleet membership.
settle_fork() {
  local env_id="$1" try body present missing v
  body=$(gql_body "$SETTLE_QUERY" "$(jq -n --arg e "$env_id" '{e: $e}')")

  for try in $(seq 1 "$SETTLE_ATTEMPTS"); do
    if graphql_try "$body" "listing service instances in environment $env_id"; then
      present=$(echo "$GQL_RESPONSE" | jq -r '.data.environment.serviceInstances.edges[]?.node.serviceId')
      missing=""
      for v in "$RAILWAY_SVC_GATEWAY_ID" "$RAILWAY_SVC_APP_ID" "$RAILWAY_SVC_LANDING_ID" \
               "$RAILWAY_SVC_OPS_CONSOLE_ID" "$RAILWAY_SVC_POSTGRES_ID"; do
        if ! echo "$present" | grep -qx "$v"; then missing="$missing $v"; fi
      done
      if [ -z "$missing" ]; then
        echo "All 5 reconciled service instances are present in $env_id (attempt $try)."
        return 0
      fi
      echo "  (settle attempt $try) still missing:$missing — retrying in ${SETTLE_INTERVAL}s ..."
    else
      echo "  (settle attempt $try) $GQL_ERROR — retrying in ${SETTLE_INTERVAL}s ..."
    fi
    sleep "$SETTLE_INTERVAL"
  done

  echo "::error::Environment $env_id did not materialise its service instances within $((SETTLE_ATTEMPTS * SETTLE_INTERVAL))s — still missing:$missing. This is NOT evidence that the development environment drifted; the Watch-Paths assertion would misreport an unmaterialised fork as exactly that."
  exit 1
}

# --- Reconcile C: domains ----------------------------------------------------
#
# F7 is absolute: this NEVER emits a URL. It only makes domains EXIST. The
# untouched `urls` step remains the sole discoverer and still fails if any is
# missing, so no URL is ever constructed from a pattern.
reconcile_domain() {
  local env_id="$1" svc_id="$2" label="$3" existing target src_count input body

  graphql_post "$(gql_body "$DOMAINS_QUERY" \
    "$(jq -n --arg p "$RAILWAY_PROJECT_ID" --arg e "$env_id" --arg s "$svc_id" '{p: $p, e: $e, s: $s}')")" \
    "reading $label domains in environment $env_id"

  existing=$(echo "$GQL_RESPONSE" | jq -r '.data.domains.serviceDomains[0].domain // empty')
  if [ -n "$existing" ]; then
    echo "  $label: domain already present ($existing, targetPort=$(echo "$GQL_RESPONSE" | jq -r '.data.domains.serviceDomains[0].targetPort // "null"')) — no mutation. This is the expected path; domains were measured to carry into a fork."
    return 0
  fi

  echo "::warning::$label has NO domain in environment $env_id. Domains were measured to CARRY into a fork, so this is the unexpected branch — creating one as insurance."

  # AC #4: targetPort is READ from `development`, never hardcoded.
  graphql_post "$(gql_body "$DOMAINS_QUERY" \
    "$(jq -n --arg p "$RAILWAY_PROJECT_ID" --arg e "$RAILWAY_DEV_ENVIRONMENT_ID" --arg s "$svc_id" '{p: $p, e: $e, s: $s}')")" \
    "reading the $label targetPort from the source environment $RAILWAY_DEV_ENVIRONMENT_ID"

  src_count=$(echo "$GQL_RESPONSE" | jq '[.data.domains.serviceDomains[]?] | length')
  if [ "$src_count" = "0" ]; then
    echo "::error::$label (service $svc_id) has no domain in the SOURCE environment $RAILWAY_DEV_ENVIRONMENT_ID either, so there is no source of truth for its targetPort. Refusing to hardcode one. Give it a domain per docs/add-a-service.md step 6."
    exit 1
  fi
  target=$(echo "$GQL_RESPONSE" | jq -r '.data.domains.serviceDomains[0].targetPort // empty')

  if [ -n "$target" ]; then
    input=$(jq -n --arg e "$env_id" --arg s "$svc_id" --argjson t "$target" \
      '{input: {environmentId: $e, serviceId: $s, targetPort: $t}}')
    echo "  $label: creating a domain with targetPort=$target, read from the source environment."
  else
    # MEASURED: targetPort is null on the gateway domain in BOTH the fork and
    # `development`. Null is the normal state, so this is NOT a failure — it is
    # Railway's magic-port detection, which is exactly what the source
    # environment relies on. Replicate that by OMITTING targetPort. Hardcoding
    # 8080 here would invent a value the source environment does not have.
    input=$(jq -n --arg e "$env_id" --arg s "$svc_id" '{input: {environmentId: $e, serviceId: $s}}')
    echo "  $label: the source environment domain has a null targetPort (measured normal — Railway magic-port detection), so targetPort is OMITTED rather than invented."
  fi

  body=$(gql_body "$DOMAIN_CREATE_MUTATION" "$input")
  graphql_post "$body" "creating a $label domain in environment $env_id"

  # Never trust the mutation's own selection set — re-query independently, the
  # same discipline cmd_disable_pr_environments applies to projectUpdate.
  sleep 5
  graphql_post "$(gql_body "$DOMAINS_QUERY" \
    "$(jq -n --arg p "$RAILWAY_PROJECT_ID" --arg e "$env_id" --arg s "$svc_id" '{p: $p, e: $e, s: $s}')")" \
    "re-reading $label domains in environment $env_id after create"

  existing=$(echo "$GQL_RESPONSE" | jq -r '.data.domains.serviceDomains[0].domain // empty')
  if [ -z "$existing" ]; then
    echo "::error::serviceDomainCreate reported success for $label (service $svc_id) in environment $env_id but an INDEPENDENT re-query still finds no domain. The urls step below would fail to discover it."
    exit 1
  fi
  echo "  $label: created and confirmed by re-query ($existing)."
}

reconcile_domains() {
  local env_id="$1"
  echo "Reconciling domains for the 4 public services in $env_id ..."
  reconcile_domain "$env_id" "$RAILWAY_SVC_GATEWAY_ID" gateway
  reconcile_domain "$env_id" "$RAILWAY_SVC_APP_ID" app
  reconcile_domain "$env_id" "$RAILWAY_SVC_LANDING_ID" landing
  reconcile_domain "$env_id" "$RAILWAY_SVC_OPS_CONSOLE_ID" ops-console
}

# --- Reconcile D: Postgres bring-up ------------------------------------------
#
# wait_for_postgres <env-id> <deployment-id|"">
#
# THE SINGLE MOST IMPORTANT CONSTRAINT IN THIS FILE.
#
# MEASURED: Postgres reports CRASHED TRANSIENTLY mid-startup and then settles to
# SUCCESS. A poll that breaks on the first CRASHED reports a FALSE FATAL
# FAILURE — precisely the mistake made during the probe run, where a
# story-blocking finding was reported for a deployment that read SUCCESS moments
# later.
#
# Therefore this loop has NO early-exit on a bad status. It polls until the
# status is SUCCESS or until the attempt budget is exhausted, and only then
# fails. Nothing here may be "optimised" into an early break: one probe does not
# bound how long CRASHED can persist, so ANY early-fail threshold is a guess and
# every guess reintroduces the false fatal.
wait_for_postgres() {
  local env_id="$1" dep_id="$2" try status last="" seq="" body

  if [ -n "$dep_id" ]; then
    body=$(gql_body "$DEPLOYMENT_STATUS_QUERY" "$(jq -n --arg id "$dep_id" '{id: $id}')")
  else
    body=$(gql_body "$SERVICE_INSTANCE_QUERY" \
      "$(jq -n --arg e "$env_id" --arg s "$RAILWAY_SVC_POSTGRES_ID" '{e: $e, s: $s}')")
  fi

  for try in $(seq 1 "$PG_WAIT_ATTEMPTS"); do
    # A transport blip must not end the wait either — retry on the next tick.
    if graphql_try "$body" "polling the postgres deployment status in environment $env_id"; then
      if [ -n "$dep_id" ]; then
        status=$(echo "$GQL_RESPONSE" | jq -r '.data.deployment.status // "UNKNOWN"')
      else
        status=$(echo "$GQL_RESPONSE" | jq -r '.data.serviceInstance.latestDeployment.status // "UNKNOWN"')
      fi
    else
      status="UNREADABLE"
    fi

    if [ "$status" != "$last" ]; then
      seq="${seq:+$seq -> }$status"
      last="$status"
      echo "  (postgres poll $try) status=$status"
    fi

    if [ "$status" = "SUCCESS" ]; then
      echo "postgres reached SUCCESS in environment $env_id (observed: $seq)."
      return 0
    fi

    # DO NOT add a break for CRASHED/FAILED/REMOVED here. See the header above.
    sleep "$PG_WAIT_INTERVAL"
  done

  echo "::error::postgres (service $RAILWAY_SVC_POSTGRES_ID, deployment ${dep_id:-<none>}) in environment $env_id never reached SUCCESS within $((PG_WAIT_ATTEMPTS * PG_WAIT_INTERVAL))s. Last status: $last. Observed sequence: $seq. This is a POSTGRES failure, not a generic timeout — the gateway would be unable to connect and migrations would never run."
  exit 1
}

# MEASURED: Postgres in a fresh fork has latestDeployment == NONE. Nothing in
# this repo has ever deployed Postgres (the `railway up` matrices are gateway +
# 7 contexts + 3 SPAs, and the Watch-Paths assertion excludes it); it works in
# `development` only because it has been running there since M1. So a fork needs
# an EXPLICIT deploy, and this is where it happens.
ensure_postgres_running() {
  local env_id="$1" status dep_id

  graphql_post "$(gql_body "$SERVICE_INSTANCE_QUERY" \
    "$(jq -n --arg e "$env_id" --arg s "$RAILWAY_SVC_POSTGRES_ID" '{e: $e, s: $s}')")" \
    "reading the postgres service instance in environment $env_id"

  if echo "$GQL_RESPONSE" | jq -e '.data.serviceInstance == null' >/dev/null 2>&1; then
    echo "::error::postgres (service $RAILWAY_SVC_POSTGRES_ID) has no service instance in environment $env_id. The fork did not receive it."
    exit 1
  fi

  status=$(echo "$GQL_RESPONSE" | jq -r '.data.serviceInstance.latestDeployment.status // "NONE"')
  dep_id=$(echo "$GQL_RESPONSE" | jq -r '.data.serviceInstance.latestDeployment.id // empty')
  echo "postgres in $env_id: latestDeployment status=$status id=${dep_id:-<none>}"

  case "$status" in
    SUCCESS)
      echo "postgres already has a successful deployment — no mutation. The pg_isready probe is the real liveness proof."
      return 0
      ;;
    SLEEPING)
      echo "::warning::postgres reports SLEEPING in $env_id. Not redeploying — Railway wakes it on connect, and the pg_isready probe is the authoritative test."
      return 0
      ;;
    NEEDS_APPROVAL)
      echo "::error::postgres in $env_id reports NEEDS_APPROVAL, meaning the fork STAGED its changes instead of committing them. That contradicts the omitted-stageInitialChanges assumption environmentCreate relies on (M4-23-03). Refusing to auto-approve: this is a finding about how forks now behave, not something to repair blind."
      exit 1
      ;;
    SKIPPED)
      echo "::error::postgres in $env_id reports SKIPPED — the likeliest cause is a non-empty Watch Path on the Postgres service, which makes Railway silently decline the deployment."
      exit 1
      ;;
    NONE)
      # The measured case: skipInitialDeploys:true leaves Postgres with zero
      # deployments and no running container, which no other workflow step
      # would ever start.
      echo "postgres has NEVER been deployed in $env_id (the measured skipInitialDeploys result) — deploying it explicitly ..."
      ;;
    FAILED|REMOVED)
      # Deliberately NOT including CRASHED here: CRASHED was measured to be a
      # TRANSIENT mid-startup state, so redeploying on it would abort a
      # deployment that was about to succeed. CRASHED falls through to the
      # poll instead, which is what actually distinguishes the two.
      echo "::warning::postgres reports $status in $env_id — redeploying it."
      if graphql_try "$(gql_body "$SERVICE_REDEPLOY_MUTATION" \
        "$(jq -n --arg e "$env_id" --arg s "$RAILWAY_SVC_POSTGRES_ID" '{e: $e, s: $s}')")" \
        "redeploying postgres in environment $env_id"; then
        wait_for_postgres "$env_id" ""
        return 0
      fi
      echo "::error::serviceInstanceRedeploy failed for postgres in environment $env_id: $GQL_ERROR"
      exit 1
      ;;
    *)
      # BUILDING / DEPLOYING / QUEUED / INITIALIZING / WAITING / CRASHED —
      # already in flight or still settling. No mutation; let the poll decide.
      echo "postgres is already in flight or still settling (status=$status) — polling rather than mutating."
      wait_for_postgres "$env_id" "$dep_id"
      return 0
      ;;
  esac

  if ! graphql_try "$(gql_body "$SERVICE_DEPLOY_MUTATION" \
    "$(jq -n --arg e "$env_id" --arg s "$RAILWAY_SVC_POSTGRES_ID" '{e: $e, s: $s}')")" \
    "deploying postgres in environment $env_id"; then
    echo "::warning::serviceInstanceDeployV2 failed for postgres in $env_id ($GQL_ERROR) — falling back once to serviceInstanceRedeploy."
    if ! graphql_try "$(gql_body "$SERVICE_REDEPLOY_MUTATION" \
      "$(jq -n --arg e "$env_id" --arg s "$RAILWAY_SVC_POSTGRES_ID" '{e: $e, s: $s}')")" \
      "redeploying postgres in environment $env_id"; then
      echo "::error::Both serviceInstanceDeployV2 and serviceInstanceRedeploy failed for postgres (service $RAILWAY_SVC_POSTGRES_ID) in environment $env_id: $GQL_ERROR"
      exit 1
    fi
    echo "serviceInstanceRedeploy worked where serviceInstanceDeployV2 did not — worth recording in docs/deploy-model.md."
    wait_for_postgres "$env_id" ""
    return 0
  fi

  dep_id=$(echo "$GQL_RESPONSE" | jq -r '.data.serviceInstanceDeployV2 // empty')
  echo "serviceInstanceDeployV2 returned deployment id ${dep_id:-<none>} for postgres in $env_id."
  wait_for_postgres "$env_id" "$dep_id"
}

# --- Reconcile E: volume (OBSERVE ONLY — never fails) ------------------------
#
# MEASURED: a fork gets NO volume (volumeInstances == []) while `development`
# has a 5000MB one, and Postgres deploys, reaches SUCCESS and accepts TCP
# connections regardless. The design's "absent volume = fail loudly" check would
# therefore have failed EVERY run. It is intentionally not implemented.
#
# A PR database is EPHEMERAL BY DESIGN, and that is the correct shape: the
# gateway bootstraps, migrates and seeds at boot (M4-21-04), so a PR database is
# meant to be born empty. Recorded as an observation for the run log only.
observe_volumes() {
  local env_id="$1" fork_count dev_count

  graphql_post "$(gql_body "$VOLUMES_QUERY" "$(jq -n --arg e "$env_id" '{e: $e}')")" \
    "reading volume instances in environment $env_id"
  fork_count=$(echo "$GQL_RESPONSE" | jq --arg s "$RAILWAY_SVC_POSTGRES_ID" \
    '[.data.environment.volumeInstances.edges[]?.node | select(.serviceId == $s)] | length')

  if [ "$fork_count" != "0" ]; then
    echo "postgres volume in $env_id:"
    echo "$GQL_RESPONSE" | jq -r --arg s "$RAILWAY_SVC_POSTGRES_ID" \
      '.data.environment.volumeInstances.edges[]?.node | select(.serviceId == $s)
       | "  mountPath=\(.mountPath) sizeMB=\(.sizeMB) currentSizeMB=\(.currentSizeMB) state=\(.state // "null")"'
    return 0
  fi

  graphql_post "$(gql_body "$VOLUMES_QUERY" "$(jq -n --arg e "$RAILWAY_DEV_ENVIRONMENT_ID" '{e: $e}')")" \
    "reading volume instances in the source environment $RAILWAY_DEV_ENVIRONMENT_ID"
  dev_count=$(echo "$GQL_RESPONSE" | jq --arg s "$RAILWAY_SVC_POSTGRES_ID" \
    '[.data.environment.volumeInstances.edges[]?.node | select(.serviceId == $s)] | length')

  echo "OBSERVATION: environment $env_id received NO postgres volume (source environment has $dev_count). This is EXPECTED and is NOT a failure — measured 2026-07-18: a fork gets no volume and Postgres still deploys and accepts connections. A PR database is ephemeral by design; the gateway bootstraps, migrates and seeds it at boot."
}

# --- Reconcile F: TCP proxy (OBSERVE ONLY — never fails) ---------------------
#
# MEASURED: the TCP proxy CARRIES into a fork with its own distinct port, and
# both DATABASE_PUBLIC_URL and DATABASE_URL resolve. No reconciliation is needed
# in practice.
#
# The create path is deliberately NOT implemented: `tcpProxyCreate` is DEPRECATED
# ("Use staged changes and apply them ... requires you to redeploy the service for
# it to be active"), so calling it blind would both stage work this step does not
# apply and depend on a field Railway may remove. If the proxy is ever genuinely
# absent, the pg_isready probe in dev-env.yml fails loudly and names it — that
# probe is the authoritative liveness gate, not this observation.
#
# M4-22: "Close the Public Database Door" intends to remove the
# DATABASE_PUBLIC_URL dependency entirely. When it lands, this observation and
# the pg_isready probe go with it.
observe_tcp_proxy() {
  local env_id="$1" count

  graphql_post "$(gql_body "$TCP_PROXIES_QUERY" \
    "$(jq -n --arg e "$env_id" --arg s "$RAILWAY_SVC_POSTGRES_ID" '{e: $e, s: $s}')")" \
    "reading postgres TCP proxies in environment $env_id"

  count=$(echo "$GQL_RESPONSE" | jq '[.data.tcpProxies[]?] | length')
  if [ "$count" = "0" ]; then
    echo "::warning::No postgres TCP proxy found in environment $env_id. Measured behaviour is that it CARRIES into a fork, so this is unexpected. NOT auto-created: tcpProxyCreate is deprecated in favour of staged changes and needs a redeploy to activate. The pg_isready probe is the authoritative gate and will fail naming DATABASE_PUBLIC_URL if this really is broken."
    return 0
  fi
  echo "postgres TCP proxy in $env_id:"
  echo "$GQL_RESPONSE" | jq -r '.data.tcpProxies[]? | "  domain=\(.domain) proxyPort=\(.proxyPort) applicationPort=\(.applicationPort) syncStatus=\(.syncStatus)"'
}

# --- ENVIRONMENT audit (RECORD ONLY — sets nothing) --------------------------
#
# MEASURED: `ENVIRONMENT` in a fork resolves to the literal `development`
# (inherited verbatim), while RAILWAY_ENVIRONMENT_NAME is `pr-<N>`. Consequence
# is documented in docs/deploy-model.md. Nothing is set here
# ([env-name-is-convention]) and no app code is touched.
record_environment_variable() {
  local env_id="$1" env_value rw_value

  graphql_post "$(gql_body "$SERVICE_VARIABLES_QUERY" \
    "$(jq -n --arg p "$RAILWAY_PROJECT_ID" --arg e "$env_id" --arg s "$RAILWAY_SVC_GATEWAY_ID" '{p: $p, e: $e, s: $s}')")" \
    "reading the gateway variables in environment $env_id"

  # Only the two named keys are read out. The map also carries live credentials,
  # so it is never logged wholesale.
  env_value=$(echo "$GQL_RESPONSE" | jq -r '.data.variables.ENVIRONMENT // "<unset>"')
  rw_value=$(echo "$GQL_RESPONSE" | jq -r '.data.variables.RAILWAY_ENVIRONMENT_NAME // "<unset>"')

  echo "RECORD (no variable is set): gateway ENVIRONMENT=$env_value RAILWAY_ENVIRONMENT_NAME=$rw_value in $env_id."
  if [ "$env_value" = "development" ]; then
    echo "  As documented in docs/deploy-model.md, ENVIRONMENT is inherited verbatim from the source environment, so the fail-closed provisioning allowlist is DECORATIVE in a fork: it passes on its == \"development\" branch rather than its pr-<N> branch. It retains full value on the paths it was written for (production, staging, empty)."
  else
    echo "::warning::gateway ENVIRONMENT resolved to '$env_value', not the measured-expected literal 'development'. Recorded, not repaired — update docs/deploy-model.md if fork inheritance has changed."
  fi
}

# cmd_reconcile_fork <environment-id>
# The five reconciles are one command on purpose: they are strictly sequential,
# share the fork environment id and the auth context, and produce one coherent
# CI step.
cmd_reconcile_fork() {
  local env_id="${1:-}"
  if [ -z "$env_id" ]; then
    echo "::error::usage: railway-env.sh reconcile-fork <environment-id>"
    exit 2
  fi
  require_env
  require_source_env
  require_fork_ids

  if [ "$env_id" = "$RAILWAY_DEV_ENVIRONMENT_ID" ]; then
    echo "::error::Refusing to run fork reconciliation against the persistent development environment ($env_id). It is not a fork ([development-not-ephemeral]); this command only ever targets an ephemeral pr-<N> environment."
    exit 1
  fi

  echo "Reconciling fork fidelity for environment $env_id ..."
  settle_fork "$env_id"
  reconcile_domains "$env_id"
  ensure_postgres_running "$env_id"
  observe_volumes "$env_id"
  observe_tcp_proxy "$env_id"
  record_environment_variable "$env_id"
  echo "Fork reconciliation complete for $env_id."
}

case "${1:-}" in
  assert-project-settings)   cmd_assert_project_settings ;;
  disable-pr-environments)   cmd_disable_pr_environments ;;
  ensure-environment)        cmd_ensure_environment "${2:-}" ;;
  audit-sealed-variables)    cmd_audit_sealed_variables ;;
  reconcile-fork)            cmd_reconcile_fork "${2:-}" ;;
  delete-environment)        cmd_delete_environment "${2:-}" ;;
  *)
    echo "::error::usage: railway-env.sh <assert-project-settings|disable-pr-environments|ensure-environment <name>|audit-sealed-variables|reconcile-fork <environment-id>|delete-environment <name>>"
    exit 2
    ;;
esac
