#!/usr/bin/env bash
# scripts/ci/railway-env.sh <assert-project-settings|disable-pr-environments|ensure-environment <name>>
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

case "${1:-}" in
  assert-project-settings)   cmd_assert_project_settings ;;
  disable-pr-environments)   cmd_disable_pr_environments ;;
  ensure-environment)        cmd_ensure_environment "${2:-}" ;;
  *)
    echo "::error::usage: railway-env.sh <assert-project-settings|disable-pr-environments|ensure-environment <name>>"
    exit 2
    ;;
esac
