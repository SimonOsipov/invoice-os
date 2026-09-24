#!/usr/bin/env bash
# scripts/ci/idp-up.sh <auth-admin-dsn> <db-host-port>
#
# Builds sidecar/auth/Dockerfile and starts idp-es256, idp-hs256 and idp-rebuild on 9991-9993.
# Stdout carries only the three NAME=url lines and IDP_ISSUER (safe for $GITHUB_ENV); the rest goes to stderr.
# The DSN must be supabase_auth_admin's: a superuser would hide a missing grant or search_path.
set -euo pipefail

dsn="${1:?usage: idp-up.sh <auth-admin-dsn> <db-host-port>}"
db_port="${2:?usage: idp-up.sh <auth-admin-dsn> <db-host-port>}"
# One issuer for every container; the tests read it back as IDP_ISSUER.
issuer="urn:ascomply:auth:ci"
cd "$(git rev-parse --show-toplevel)"

case "$dsn" in
  *//supabase_auth_admin:*) ;;
  *) echo "::error::idp-up.sh needs the supabase_auth_admin DSN" >&2; exit 2 ;;
esac

# Linux (CI) shares the host network; Docker Desktop reaches the host DB through host.docker.internal.
if [ "$(uname -s)" = Linux ]; then
  net=(--network host)
else
  net=()
  dsn="${dsn/@localhost:$db_port\//@host.docker.internal:$db_port/}"
  dsn="${dsn/@127.0.0.1:$db_port\//@host.docker.internal:$db_port/}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
go build -o "$tmp/prenv" ./tools/prenv >&2

docker build -q -f sidecar/auth/Dockerfile -t idp:ci sidecar/auth >&2

# start <name> <port> [extra docker args...]; secrets pass by name so they never reach argv.
start() {
  local name="$1" port="$2"
  shift 2
  docker rm -f "$name" >/dev/null 2>&1 || true
  local ports=()
  if [ ${#net[@]} -eq 0 ]; then ports=(-p "$port:$port"); fi
  DATABASE_URL="$dsn" GOTRUE_JWT_SECRET="$(openssl rand -hex 32)" \
    docker run -d --name "$name" ${net[@]+"${net[@]}"} ${ports[@]+"${ports[@]}"} \
      -e DATABASE_URL -e GOTRUE_JWT_SECRET \
      -e PORT="$port" \
      -e API_EXTERNAL_URL="http://localhost:$port" \
      -e GOTRUE_SITE_URL="http://localhost:3000" \
      -e GOTRUE_JWT_ISSUER="$issuer" \
      -e GOTRUE_DISABLE_SIGNUP=false \
      -e GOTRUE_MAILER_AUTOCONFIRM=true \
      -e GOTRUE_SMTP_HOST= \
      "$@" idp:ci >/dev/null

  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "http://localhost:$port/health" 2>/dev/null; then
      return 0
    fi
    if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]; then
      break
    fi
    sleep 1
  done
  echo "::error::$name did not answer /health on port $port" >&2
  docker logs --tail 50 "$name" >&2 || true
  exit 1
}

# Exports a fresh key for the next start; docker reads it by name.
es256_keys() {
  "$tmp/prenv" jwk-es256 >"$tmp/keys"
  "$tmp/prenv" jwk-check <"$tmp/keys" >&2
  GOTRUE_JWT_KEYS="$(cat "$tmp/keys")"
  export GOTRUE_JWT_KEYS
}

# Sequential: each GoTrue runs its migrations on boot, so the first finishes them alone.
es256_keys
start idp-es256 9991 -e GOTRUE_JWT_KEYS
start idp-hs256 9992
es256_keys
start idp-rebuild 9993 -e GOTRUE_JWT_KEYS \
  -e GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_URI=pg-functions://postgres/public/test_rebuild_claims_hook

echo "IDP_ES256_URL=http://localhost:9991"
echo "IDP_HS256_URL=http://localhost:9992"
echo "IDP_REBUILD_URL=http://localhost:9993"
echo "IDP_ISSUER=$issuer"
