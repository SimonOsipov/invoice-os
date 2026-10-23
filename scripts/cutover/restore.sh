#!/bin/sh
# restore.sh — runs in the Cloud Run job `cutover` (postgres:18.6-alpine), inside the production
# network. cutover.sh uploads it next to the dump; the bucket is mounted at /cutover.
#
#   restore.sh recreate   drop and recreate the invoice_os database (as postgres)
#   restore.sh restore    restore auth as supabase_auth_admin, then public as invoice_migrator
#   restore.sh grants     re-apply grants Railway skipped because the role did not exist yet
#   restore.sh check      run db/restore-check.sql for each tenant in tenants.txt; write check/<tenant>.txt
#
# The job's environment holds DATABASE_SUPERUSER_URL, DATABASE_MIGRATION_URL and DATABASE_AUTH_URL.
set -eu

dir=$(dirname "$0")
dump="$dir/railway.dump"

# list <owner regex>: the dump's TOC entries for those owners, without schema creation. The
# bootstrap creates both schemas and grants on them.
list() {
  pg_restore -l "$dump" | grep -v '^;' | grep -E " ($1)\$" | grep -v ' SCHEMA - '
}

case "${1:-}" in
  recreate)
    admin="${DATABASE_SUPERUSER_URL%%/invoice_os*}/postgres${DATABASE_SUPERUSER_URL#*/invoice_os}"
    psql "$admin" -v ON_ERROR_STOP=1 -c 'DROP DATABASE IF EXISTS invoice_os WITH (FORCE)' -c 'CREATE DATABASE invoice_os'
    ;;
  restore)
    list 'supabase_auth_admin' >/tmp/auth.list
    list 'invoice_migrator|staff_reader|auth_hook_reader' >/tmp/public.list
    echo "restoring $(wc -l </tmp/auth.list) auth and $(wc -l </tmp/public.list) public entries"
    # public.list's ALTER ... OWNER TO staff_reader needs invoice_migrator's SET on that role (db/bootstrap.sql).
    pg_restore --single-transaction --exit-on-error -L /tmp/auth.list -d "$DATABASE_AUTH_URL" "$dump"
    pg_restore --single-transaction --exit-on-error -L /tmp/public.list -d "$DATABASE_MIGRATION_URL" "$dump"
    echo "restored"
    ;;
  grants)
    # 20261022102834_gateway_handoff_codes grants only when invoice_gateway exists; on Railway it did not.
    psql "$DATABASE_MIGRATION_URL" -v ON_ERROR_STOP=1 -c 'GRANT SELECT, INSERT, DELETE ON gateway_handoff_codes TO invoice_gateway'
    ;;
  check)
    cutoff=$(cat "$dir/cutoff.txt")
    psql "$DATABASE_SUPERUSER_URL" -v ON_ERROR_STOP=1 -q -f "$dir/restore-auditor.sql"
    mkdir -p "$dir/check"
    while read -r tenant; do
      { echo "SET ROLE restore_auditor;"
        echo "SET restore_check.tenant = '$tenant';"
        echo "SET restore_check.cutoff = '$cutoff';"
        cat "$dir/restore-check.sql"; } |
        psql "$DATABASE_SUPERUSER_URL" -v ON_ERROR_STOP=1 -qAt >"$dir/check/$tenant.txt"
    done <"$dir/tenants.txt"
    psql "$DATABASE_AUTH_URL" -At -c 'SELECT count(*) FROM auth.users' >"$dir/check/auth-users.txt"
    echo "checked $(wc -l <"$dir/tenants.txt") tenants"
    ;;
  *)
    echo "usage: restore.sh recreate|restore|grants|check" >&2
    exit 2
    ;;
esac
