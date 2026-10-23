#!/usr/bin/env bash
# cutover.sh — GKE-10: move production data from Railway to Google Cloud. Run from the repo root on
# a laptop with `railway` linked to the ASComply project and gcloud signed in as a project owner.
# The runbook is in the vault: GKE-10 Production moves to Google Cloud in one window.
#
#   cutover.sh dump        dump Railway and fingerprint each tenant there; prints the run directory
#   cutover.sh restore <run>    upload, recreate invoice_os, bootstrap, restore, compare, migrate
#   cutover.sh documents   copy the Railway documents bucket to Cloud Storage, keys unchanged
#
# A run directory holds customer data. It lives under /tmp/cutover with mode 700; delete it after.
set -euo pipefail

PROJECT=ascomply-prod
REGION=africa-south1
BUCKET=ascomply-prod-cutover
# shellcheck disable=SC2016 # the Railway container expands $PGUSER and $PGDATABASE
PSQL_RAILWAY='psql -U "$PGUSER" -d "$PGDATABASE" -v ON_ERROR_STOP=1 -qAt'
PG_RESTORE=$(command -v pg_restore || echo /opt/homebrew/opt/libpq/bin/pg_restore)

railway_sql() {
  railway ssh --service Postgres --environment production "$PSQL_RAILWAY" 2> >(grep -v 'SSH key' >&2)
}

run_job() {
  echo "== job $1 ${2:-}"
  local args=()
  [ -n "${2:-}" ] && args=(--args "$2")
  gcloud run jobs execute "$1" --project "$PROJECT" --region "$REGION" --wait "${args[@]}"
}

# fingerprint <dir> <tenant>: db/restore-check.sql on Railway. The role line names the caller and differs by side.
fingerprint() {
  { echo "SET restore_check.tenant = '$2';"
    echo "SET restore_check.cutoff = '$(cat "$1/cutoff.txt")';"
    cat db/restore-check.sql; } | railway_sql | grep -v '^role |'
}

case "${1:-}" in
  dump)
    run=$(date -u +%Y%m%dT%H%M%SZ)
    dir=/tmp/cutover/$run
    mkdir -p "$dir/railway" && chmod -R 700 /tmp/cutover
    railway_sql >"$dir/cutoff.txt" <<'SQL'
SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
SQL
    # shellcheck disable=SC2016
    railway ssh --service Postgres --environment production \
      'pg_dump -U "$PGUSER" -d "$PGDATABASE" -Fc | base64 -w0' 2> >(grep -v 'SSH key' >&2) >"$dir/railway.b64"
    base64 -d -i "$dir/railway.b64" -o "$dir/railway.dump" && rm "$dir/railway.b64"
    "$PG_RESTORE" -l "$dir/railway.dump" >/dev/null
    echo "SELECT DISTINCT tenant_id FROM invoices WHERE created_at <= '$(cat "$dir/cutoff.txt")' ORDER BY 1" | railway_sql >"$dir/tenants.txt"
    while read -r tenant; do fingerprint "$dir" "$tenant" >"$dir/railway/$tenant.txt"; done <"$dir/tenants.txt"
    echo 'SELECT count(*) FROM auth.users' | railway_sql >"$dir/railway/auth-users.txt"
    echo "dumped $(wc -c <"$dir/railway.dump") bytes at $(cat "$dir/cutoff.txt"); $(wc -l <"$dir/tenants.txt") tenants fingerprinted"
    echo "run: $run"
    ;;
  restore)
    run="${2:?usage: cutover.sh restore <run>}"
    dir=/tmp/cutover/$run
    gcloud storage cp --project "$PROJECT" "$dir/railway.dump" "$dir/cutoff.txt" "$dir/tenants.txt" \
      scripts/cutover/restore.sh db/restore-check.sql db/restore-auditor.sql "gs://$BUCKET/$run/"
    run_job cutover "/cutover/$run/restore.sh,recreate"
    run_job migrate-bootstrap
    run_job cutover "/cutover/$run/restore.sh,restore"
    run_job cutover "/cutover/$run/restore.sh,check"
    rm -rf "$dir/gcp" && mkdir -p "$dir/gcp"
    gcloud storage cp --project "$PROJECT" "gs://$BUCKET/$run/check/*" "$dir/gcp/"
    for f in "$dir"/railway/*.txt; do
      if ! diff <(grep -v '^role |' "$f") <(grep -v '^role |' "$dir/gcp/$(basename "$f")") >/dev/null; then
        echo "::error::$(basename "$f" .txt) differs between Railway and Cloud SQL; nothing migrated." >&2
        exit 1
      fi
    done
    echo "every tenant fingerprint and the auth.users count match"
    # After the comparison: a new migration may add columns that change the row fingerprint.
    run_job migrate
    ;;
  documents)
    # Credentials go to rclone through the environment only; nothing is printed or written.
    vars=$(railway variables --service invoice --environment production --json)
    export RCLONE_CONFIG_RAILWAY_TYPE=s3 RCLONE_CONFIG_RAILWAY_PROVIDER=Other
    RCLONE_CONFIG_RAILWAY_ENDPOINT=$(jq -r .DOCUMENT_ENDPOINT <<<"$vars")
    RCLONE_CONFIG_RAILWAY_REGION=$(jq -r .DOCUMENT_REGION <<<"$vars")
    RCLONE_CONFIG_RAILWAY_ACCESS_KEY_ID=$(jq -r .DOCUMENT_ACCESS_KEY_ID <<<"$vars")
    RCLONE_CONFIG_RAILWAY_SECRET_ACCESS_KEY=$(jq -r .DOCUMENT_SECRET_ACCESS_KEY <<<"$vars")
    src_bucket=$(jq -r .DOCUMENT_BUCKET <<<"$vars")
    export RCLONE_CONFIG_RAILWAY_ENDPOINT RCLONE_CONFIG_RAILWAY_REGION RCLONE_CONFIG_RAILWAY_ACCESS_KEY_ID RCLONE_CONFIG_RAILWAY_SECRET_ACCESS_KEY
    export RCLONE_CONFIG_GCS_TYPE=s3 RCLONE_CONFIG_GCS_PROVIDER=GCS RCLONE_CONFIG_GCS_ENDPOINT=https://storage.googleapis.com
    RCLONE_CONFIG_GCS_ACCESS_KEY_ID=$(gcloud secrets versions access latest --secret document-access-key-id --project "$PROJECT")
    RCLONE_CONFIG_GCS_SECRET_ACCESS_KEY=$(gcloud secrets versions access latest --secret document-secret-key --project "$PROJECT")
    export RCLONE_CONFIG_GCS_ACCESS_KEY_ID RCLONE_CONFIG_GCS_SECRET_ACCESS_KEY
    rclone copy "railway:$src_bucket" gcs:ascomply-prod-documents --stats-one-line --stats 0
    rclone check "railway:$src_bucket" gcs:ascomply-prod-documents --size-only --one-way
    ;;
  *)
    echo "usage: cutover.sh dump | restore <run> | documents" >&2
    exit 2
    ;;
esac
