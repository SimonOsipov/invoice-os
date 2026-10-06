#!/usr/bin/env bash
# One-shot AC-1 probe (INFRA-05-01): do aliased fields apply, and how does Railway count
# them against the rate limit? Writes only to a probe-pr-<N> fork. Deleted by INFRA-05-08.
#   railway-batch-probe.sh <environment-id>
set -euo pipefail

RAILWAY_GRAPHQL_URL="${RAILWAY_GRAPHQL_URL:-https://backboard.railway.com/graphql/v2}"
BAD_SVC=00000000-0000-0000-0000-000000000000
RUN_ID="${GITHUB_RUN_ID:-local}"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

# shellcheck disable=SC2016  # GraphQL variables, not shell expansions.
ENV_LIST_QUERY='query envList($p: String!) {
  environments(projectId: $p) {
    edges { node { id name isEphemeral } }
  }
}'
# shellcheck disable=SC2016
SETTLE_QUERY='query settle($e: String!) {
  environment(id: $e) { serviceInstances { edges { node { serviceId serviceName } } } }
}'

RESP="" REMAINING="" RESET=""

# post <json-body-on-stdin>: sets RESP, REMAINING, RESET.
post() {
  curl -sS -D "$TMP/hdr" --data @- \
    --header "Authorization: Bearer $RAILWAY_API_TOKEN" \
    --header "Content-Type: application/json" \
    "$RAILWAY_GRAPHQL_URL" > "$TMP/resp"
  RESP=$(cat "$TMP/resp")
  REMAINING=$(sed -n 's/^[Xx]-[Rr]ate[Ll]imit-[Rr]emaining: *//Ip' "$TMP/hdr" | tr -d '\r' | tail -n 1)
  RESET=$(sed -n 's/^[Xx]-[Rr]ate[Ll]imit-[Rr]eset: *//Ip' "$TMP/hdr" | tr -d '\r' | tail -n 1)
}

env_list() {
  post < <(jq -n --arg q "$ENV_LIST_QUERY" --arg p "$RAILWAY_PROJECT_ID" '{query: $q, variables: {p: $p}}')
}

# read_body <unrendered true|false> <ids-json>: one `variables` field per service id.
read_body() {
  local n i decl="" fields=""
  n=$(jq length <<<"$2")
  for ((i = 0; i < n; i++)); do
    decl+=", \$s$i: String!"
    fields+=" a$i: variables(projectId: \$p, environmentId: \$e, serviceId: \$s$i, unrendered: $1)"
  done
  jq -n --arg q "query varsRead(\$p: String!, \$e: String!$decl) {$fields }" \
    --arg p "$RAILWAY_PROJECT_ID" --arg e "$TARGET" --argjson ids "$2" \
    '{query: $q, variables: ([$ids | to_entries[] | {key: "s\(.key)", value: .value}] | from_entries + {p: $p, e: $e})}'
}

# write_body <ids-json> <name> <value>: one `variableCollectionUpsert` field per service id.
write_body() {
  local n i decl="" fields=""
  n=$(jq length <<<"$1")
  for ((i = 0; i < n; i++)); do
    decl+="${decl:+, }\$i$i: VariableCollectionUpsertInput!"
    fields+=" a$i: variableCollectionUpsert(input: \$i$i)"
  done
  jq -n --arg q "mutation varsWrite($decl) {$fields }" --arg p "$RAILWAY_PROJECT_ID" --arg e "$TARGET" \
    --arg n "$2" --arg v "$3" --argjson ids "$1" \
    '{query: $q, variables: ([$ids | to_entries[] | {key: "i\(.key)", value: {projectId: $p, environmentId: $e, serviceId: .value, variables: {($n): $v}, skipDeploys: true}}] | from_entries)}'
}

# cost <before> <after>: the drop, or "n/a" when a header is missing or the window reset.
cost() {
  if [[ "$1" =~ ^[0-9]+$ && "$2" =~ ^[0-9]+$ && "$2" -le "$1" ]]; then echo $(($1 - $2)); else echo "n/a"; fi
}

# verdict <costs> <controls>: space-separated, index-aligned; [probe-verdict].
verdict() {
  local -a c k
  read -ra c <<<"$1"
  read -ra k <<<"$2"
  local i valid=0 big=0 one=0
  for i in "${!c[@]}"; do
    [ "${c[$i]}" != "n/a" ] && [ "${k[$i]}" != "n/a" ] || continue
    valid=$((valid + 1))
    [ "${c[$i]}" -ge 15 ] && big=$((big + 1))
    if [ "${c[$i]}" -le 2 ] && [ "${k[$i]}" -ge 1 ]; then one=$((one + 1)); fi
  done
  if [ "$one" -gt 0 ]; then echo one-call
  elif [ "$valid" -gt 0 ] && [ "$big" = "$valid" ]; then echo per-alias
  else echo inconclusive; fi
}

TARGET="${1:-}"
[ -n "$TARGET" ] || { echo "::error::usage: railway-batch-probe.sh <environment-id>"; exit 1; }
: "${RAILWAY_API_TOKEN:?}" "${RAILWAY_PROJECT_ID:?}"
if [ "$TARGET" = "${RAILWAY_DEV_ENVIRONMENT_ID:-}" ]; then
  echo "::error::refusing the persistent environment $TARGET"
  exit 1
fi

env_list
if ! jq -e --arg id "$TARGET" '[.data.environments.edges[]?.node | select(.id == $id and .isEphemeral == true and (.name | test("^probe-pr-[0-9]+$")))] | length == 1' <<<"$RESP" >/dev/null 2>&1; then
  echo "::error::$TARGET is not in the list as an ephemeral environment named probe-pr-<N>; no write was sent."
  exit 1
fi

# shellcheck disable=SC2016
post < <(jq -n --arg q "$SETTLE_QUERY" --arg e "$TARGET" '{query: $q, variables: {e: $e}}')
INSTANCES=$(jq -c '[.data.environment.serviceInstances.edges[]?.node]' <<<"$RESP")
ALL_IDS=$(jq -c 'map(.serviceId)' <<<"$INSTANCES")
W_IDS=$(jq -c 'map(select(.serviceName != "Postgres") | .serviceId)' <<<"$INSTANCES")
echo "instances: $(jq length <<<"$ALL_IDS") total, $(jq length <<<"$W_IDS") non-Postgres"
[ "$(jq length <<<"$W_IDS")" -ge 3 ] || { echo "::error::fewer than 3 non-Postgres instances"; exit 1; }

echo "== (b) reads: envList, varsRead x$(jq length <<<"$ALL_IDS"), envList"
R_COSTS="" R_CTRL=""
for round in 1 2 3; do
  env_list; r0=$REMAINING
  post < <(read_body false "$ALL_IDS"); r1=$REMAINING
  env_list; r2=$REMAINING
  cs=$(cost "$r0" "$r1") ks=$(cost "$r1" "$r2")
  echo "read round $round: remaining $r0 -> $r1 -> $r2; batched=$cs control=$ks"
  R_COSTS+="$cs " R_CTRL+="$ks "
done

echo "== (a) writes: varsWrite x$(jq length <<<"$W_IDS") then varsRead unrendered"
W_COSTS="" W_CTRL=""
for round in 1 2 3; do
  env_list; w0=$REMAINING
  post < <(write_body "$W_IDS" INFRA05_PROBE "$RUN_ID"); w1=$REMAINING
  [ "$round" != 1 ] || echo "write response: $(jq -c . <<<"$RESP" | cut -c1-400)"
  env_list; w2=$REMAINING
  cs=$(cost "$w0" "$w1") ks=$(cost "$w1" "$w2")
  echo "write round $round: remaining $w0 -> $w1 -> $w2; batched=$cs control=$ks"
  W_COSTS+="$cs " W_CTRL+="$ks "
done
post < <(read_body true "$W_IDS")
APPLIED=$(jq -r --arg v "$RUN_ID" '[.data[]? | select(.INFRA05_PROBE == $v)] | length' <<<"$RESP")
N_W=$(jq length <<<"$W_IDS")
A_ANSWER=no
[ "$APPLIED" = "$N_W" ] && A_ANSWER=yes
echo "aliased writes applied: $APPLIED of $N_W hold INFRA05_PROBE=$RUN_ID"

echo "== partial failure: A, nonexistent, B"
SVC_A=$(jq -r '.[0]' <<<"$W_IDS") SVC_B=$(jq -r '.[1]' <<<"$W_IDS")
PART_IDS=$(jq -nc --arg a "$SVC_A" --arg b "$SVC_B" --arg x "$BAD_SVC" '[$a, $x, $b]')
post < <(write_body "$PART_IDS" INFRA05_PROBE_PARTIAL "$RUN_ID")
P_DATA=$(jq -c '.data' <<<"$RESP")
P_PATHS=$(jq -c '[.errors[]? | .path]' <<<"$RESP")
echo "data=$P_DATA"
jq -c '.errors[]? | {path, code: .extensions.code, message}' <<<"$RESP"
post < <(read_body false "$(jq -nc --arg a "$SVC_A" --arg b "$SVC_B" '[$a, $b]')")
holds() { jq -r --arg k "$1" --arg v "$RUN_ID" '(.data[$k].INFRA05_PROBE_PARTIAL // "") == $v' <<<"$RESP"; }
echo "A holds value: $(holds a0); B holds value: $(holds a1)"
B_APPLIED=no
[ "$(holds a1)" != true ] || B_APPLIED=yes

echo "x-ratelimit-reset sample: $RESET"
RV=$(verdict "$R_COSTS" "$R_CTRL") WV=$(verdict "$W_COSTS" "$W_CTRL")
echo "read verdict: $RV; write verdict: $WV"
V=inconclusive
[ "$RV" != "$WV" ] || V=$RV
echo "INFRA-05 probe: (a) aliased writes applied: $A_ANSWER; (b) batched read cost per round: ${R_COSTS% } (control ${R_CTRL% }); batched write cost: ${W_COSTS% } (control ${W_CTRL% }); verdict: $V; partial failure: data=$P_DATA, paths=$P_PATHS, B applied: $B_APPLIED; x-ratelimit-reset sample: $RESET"
