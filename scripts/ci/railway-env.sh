#!/usr/bin/env bash
# scripts/ci/railway-env.sh <assert-project-settings|disable-pr-environments>
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
# Auth: account-scoped RAILWAY_API_TOKEN, `Authorization: Bearer`. A Railway *project*
# token is pinned to one environment and cannot perform projectUpdate.
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

# Response of the most recent successful GraphQL call.
GQL_RESPONSE=""

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
  *)
    echo "::error::usage: railway-env.sh <assert-project-settings|disable-pr-environments>"
    exit 2
    ;;
esac
