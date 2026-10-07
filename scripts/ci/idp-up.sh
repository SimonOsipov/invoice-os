#!/usr/bin/env bash
# scripts/ci/idp-up.sh <auth-admin-dsn> <db-host-port>
#
# Builds sidecar/auth/Dockerfile and starts idp-es256, idp-hs256 and idp-rebuild on 9991-9993,
# plus mailpit (SMTP 1025, API 8025) and idp-mail on 9994, which mails its branded confirmation and reset links there,
# and idp-short on 9996, whose access tokens expire after 5 s (9995 is the mailed-link verify handler).
# Stdout carries only the NAME=url lines and IDP_ISSUER (safe for $GITHUB_ENV); the rest goes to stderr.
# IDP_SLOT=n (0-500, default 0) suffixes every container name with -s<n> and adds 10*n to every
# port, so two worktrees' runs do not remove each other's containers; `make test-idp` derives it.
# The DSN must be supabase_auth_admin's: a superuser would hide a missing grant or search_path.
set -euo pipefail

dsn="${1:?usage: idp-up.sh <auth-admin-dsn> <db-host-port>}"
db_port="${2:?usage: idp-up.sh <auth-admin-dsn> <db-host-port>}"
# One issuer for every container; the tests read it back as IDP_ISSUER.
issuer="urn:ascomply:auth:ci"
slot="${IDP_SLOT:-0}"
case "$slot" in
  *[!0-9]*) echo "::error::IDP_SLOT must be a number from 0 to 500, got '$slot'" >&2; exit 2 ;;
esac
if [ "$slot" -gt 500 ]; then echo "::error::IDP_SLOT must be a number from 0 to 500, got '$slot'" >&2; exit 2; fi
sfx=""
if [ "$slot" -gt 0 ]; then sfx="-s$slot"; fi
off=$((10 * slot))
smtp=$((1025 + off)) mail_api=$((8025 + off)) verify=$((9995 + off))
cd "$(git rev-parse --show-toplevel)"

case "$dsn" in
  *//supabase_auth_admin:*) ;;
  *) echo "::error::idp-up.sh needs the supabase_auth_admin DSN" >&2; exit 2 ;;
esac

# Linux (CI) shares the host network; Docker Desktop reaches the host DB through host.docker.internal.
if [ "$(uname -s)" = Linux ]; then
  net=(--network host)
  smtp_host=localhost
else
  net=()
  smtp_host=host.docker.internal
  dsn="${dsn/@localhost:$db_port\//@host.docker.internal:$db_port/}"
  dsn="${dsn/@127.0.0.1:$db_port\//@host.docker.internal:$db_port/}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
go build -o "$tmp/prenv" ./tools/prenv >&2

docker build -q -f sidecar/auth/Dockerfile -t idp:ci sidecar/auth >&2

# start <name> <port> [extra docker args...]; secrets pass by name so they never reach argv.
start() {
  local name="$1$sfx" port=$(($2 + off))
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

mailpit_image="axllent/mailpit:v1.31.2@sha256:74d609a42ec279aa63c6b4622a6fa9b5408d1ad5b1d76a1c4be40a265ce0863d"
start_mailpit() {
  docker rm -f "mailpit$sfx" >/dev/null 2>&1 || true
  local ports=() binds=()
  if [ ${#net[@]} -eq 0 ]; then
    ports=(-p "$smtp:1025" -p "$mail_api:8025")
  elif [ "$slot" -gt 0 ]; then
    binds=(-e "MP_SMTP_BIND_ADDR=0.0.0.0:$smtp" -e "MP_UI_BIND_ADDR=0.0.0.0:$mail_api")
  fi
  docker run -d --name "mailpit$sfx" ${net[@]+"${net[@]}"} ${ports[@]+"${ports[@]}"} ${binds[@]+"${binds[@]}"} "$mailpit_image" >/dev/null
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "http://localhost:$mail_api/readyz" 2>/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "::error::mailpit$sfx did not answer /readyz on port $mail_api" >&2
  docker logs --tail 50 "mailpit$sfx" >&2 || true
  exit 1
}

# Only idp-mail mails.
no_mail=(-e GOTRUE_MAILER_AUTOCONFIRM=true -e GOTRUE_SMTP_HOST=)

# Sequential: each GoTrue runs its migrations on boot, so the first finishes them alone.
es256_keys
start idp-es256 9991 -e GOTRUE_JWT_KEYS "${no_mail[@]}"
start idp-hs256 9992 "${no_mail[@]}"
es256_keys
start idp-rebuild 9993 -e GOTRUE_JWT_KEYS "${no_mail[@]}" \
  -e GOTRUE_HOOK_CUSTOM_ACCESS_TOKEN_URI=pg-functions://postgres/public/test_rebuild_claims_hook
start_mailpit
es256_keys
# The absolute confirmation path points the mailed link at the test's gateway verify routes, which also serve the branded template.
GOTRUE_MAILER_SUBJECTS_CONFIRMATION="Confirm your ASComply account"
export GOTRUE_MAILER_SUBJECTS_CONFIRMATION
GOTRUE_MAILER_SUBJECTS_RECOVERY="Reset your ASComply password"
export GOTRUE_MAILER_SUBJECTS_RECOVERY
# The default email-sent cap (30/h) would throttle the reset specs' mail volume.
start idp-mail 9994 -e GOTRUE_JWT_KEYS -e GOTRUE_MAILER_SUBJECTS_CONFIRMATION -e GOTRUE_MAILER_SUBJECTS_RECOVERY -e GOTRUE_RATE_LIMIT_EMAIL_SENT=100 \
  -e GOTRUE_MAILER_TEMPLATES_RECOVERY="http://$smtp_host:$verify/emails/recovery.html" -e GOTRUE_MAILER_URLPATHS_RECOVERY="http://localhost:$verify/auth/reset-password" \
  -e GOTRUE_MAILER_TEMPLATES_CONFIRMATION="http://$smtp_host:$verify/emails/confirmation.html" \
  -e GOTRUE_MAILER_AUTOCONFIRM=false \
  -e GOTRUE_SMTP_HOST="$smtp_host" -e GOTRUE_SMTP_PORT="$smtp" -e GOTRUE_SMTP_PASS=unused \
  -e GOTRUE_MAILER_URLPATHS_CONFIRMATION="http://localhost:$verify/auth/verify"
es256_keys
start idp-short 9996 -e GOTRUE_JWT_KEYS "${no_mail[@]}" -e GOTRUE_JWT_EXP=5

echo "IDP_ES256_URL=http://localhost:$((9991 + off))"
echo "IDP_HS256_URL=http://localhost:$((9992 + off))"
echo "IDP_REBUILD_URL=http://localhost:$((9993 + off))"
echo "IDP_MAIL_URL=http://localhost:$((9994 + off))"
echo "IDP_SHORT_URL=http://localhost:$((9996 + off))"
echo "MAILPIT_URL=http://localhost:$mail_api"
echo "IDP_ISSUER=$issuer"
