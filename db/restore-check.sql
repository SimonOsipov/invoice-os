-- Restore fingerprint of one tenant. Needs a BYPASSRLS role; set restore_check.tenant and restore_check.cutoff first.
-- Output is text rows, independent of the caller's TimeZone and DateStyle.
-- BEGIN READ ONLY makes the file's own statements read-only; the SET keeps the session read-only afterwards.
BEGIN READ ONLY;
SET default_transaction_read_only = on;
SET TimeZone = 'UTC';
SET DateStyle = 'ISO, YMD';

DO $guard$
DECLARE
  v_tenant text := current_setting('restore_check.tenant', true);
  v_cutoff text := current_setting('restore_check.cutoff', true);
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
    RAISE EXCEPTION 'restore-check: role % does not bypass RLS; counts would be wrong', current_user;
  END IF;
  IF coalesce(v_tenant, '') = '' THEN
    RAISE EXCEPTION 'restore-check: restore_check.tenant is unset or empty';
  END IF;
  IF coalesce(v_cutoff, '') = '' THEN
    RAISE EXCEPTION 'restore-check: restore_check.cutoff is unset or empty';
  END IF;
  IF v_tenant !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN
    RAISE EXCEPTION 'restore-check: restore_check.tenant is not a UUID';
  END IF;
  IF v_cutoff !~ '[0-9]{2}:[0-9]{2}(:[0-9]{2}([.][0-9]+)?)?[[:space:]]*(Z|z|[+-][0-9]{2}(:?[0-9]{2})?)$' THEN
    RAISE EXCEPTION 'restore-check: restore_check.cutoff needs an explicit offset (Z or +hh:mm)';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM tenants WHERE id = v_tenant::uuid) THEN
    RAISE EXCEPTION 'restore-check: tenant % not found', v_tenant;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM invoices WHERE tenant_id = v_tenant::uuid AND created_at <= v_cutoff::timestamptz) THEN
    RAISE EXCEPTION 'restore-check: tenant % has no invoices at or before %', v_tenant, v_cutoff;
  END IF;
END
$guard$;

WITH p AS (
  SELECT current_setting('restore_check.tenant')::uuid AS tenant,
         current_setting('restore_check.cutoff')::timestamptz AS cutoff
),
iso(fmt) AS (VALUES ('YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),
rows_(ord, sub, line) AS (
  SELECT 1, '', concat_ws(' | ', 'role', current_user,
         CASE WHEN rolsuper THEN 't' ELSE 'f' END, CASE WHEN rolbypassrls THEN 't' ELSE 'f' END)
    FROM pg_roles WHERE rolname = current_user
  UNION ALL
  SELECT 2, '', concat_ws(' | ', 'tenant', t.id, t.name) FROM tenants t, p WHERE t.id = p.tenant
  UNION ALL
  SELECT 3, 'invoices', concat_ws(' | ', 'table', 'invoices', count(*),
         coalesce(to_char(max(t.created_at) AT TIME ZONE 'UTC', (SELECT fmt FROM iso)), '-'),
         coalesce(md5(string_agg(md5(t::text), '' ORDER BY t.id)), '-'))
    FROM invoices t, p WHERE t.tenant_id = p.tenant AND t.created_at <= p.cutoff
  UNION ALL
  SELECT 3, 'line_items', concat_ws(' | ', 'table', 'line_items', count(*),
         coalesce(to_char(max(t.created_at) AT TIME ZONE 'UTC', (SELECT fmt FROM iso)), '-'),
         coalesce(md5(string_agg(md5(t::text), '' ORDER BY t.id)), '-'))
    FROM line_items t, p WHERE t.tenant_id = p.tenant AND t.created_at <= p.cutoff
  UNION ALL
  SELECT 3, 'documents', concat_ws(' | ', 'table', 'documents', count(*),
         coalesce(to_char(max(t.created_at) AT TIME ZONE 'UTC', (SELECT fmt FROM iso)), '-'),
         coalesce(md5(string_agg(md5(t::text), '' ORDER BY t.id)), '-'))
    FROM documents t, p WHERE t.tenant_id = p.tenant AND t.created_at <= p.cutoff
  UNION ALL
  SELECT 3, 'audit_log', concat_ws(' | ', 'table', 'audit_log', count(*),
         coalesce(to_char(max(t.created_at) AT TIME ZONE 'UTC', (SELECT fmt FROM iso)), '-'),
         coalesce(md5(string_agg(md5(t::text), '' ORDER BY t.id)), '-'))
    FROM audit_log t, p WHERE t.tenant_id = p.tenant AND t.created_at <= p.cutoff
  UNION ALL
  SELECT 3, 'memberships', concat_ws(' | ', 'table', 'memberships', count(*),
         coalesce(to_char(max(t.created_at) AT TIME ZONE 'UTC', (SELECT fmt FROM iso)), '-'),
         coalesce(md5(string_agg(md5(t::text), '' ORDER BY t.id)), '-'))
    FROM memberships t, p WHERE t.tenant_id = p.tenant AND t.created_at <= p.cutoff
  UNION ALL
  SELECT 4, to_char(r.created_at AT TIME ZONE 'UTC', 'YYYYMMDDHH24MISSUS') || r.id::text,
         concat_ws(' | ', 'recent', r.id, r.invoice_number, r.status, to_char(r.created_at AT TIME ZONE 'UTC', (SELECT fmt FROM iso)))
    FROM (SELECT i.* FROM invoices i, p WHERE i.tenant_id = p.tenant AND i.created_at <= p.cutoff
           ORDER BY i.created_at DESC, i.id DESC LIMIT 5) r
  UNION ALL
  SELECT 5, '', concat_ws(' | ', 'goose', count(*) FILTER (WHERE is_applied),
         max(version_id) FILTER (WHERE is_applied),
         md5(string_agg(version_id || ':' || is_applied, E'\n' ORDER BY id)))
    FROM goose_db_version
  UNION ALL
  SELECT 6, n.nspname || ' ' || pg_get_userbyid(c.relowner),
         concat_ws(' | ', 'owner', n.nspname, pg_get_userbyid(c.relowner), count(*))
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname IN ('public', 'auth')
   GROUP BY n.nspname, c.relowner
)
SELECT line FROM rows_
ORDER BY ord, CASE WHEN ord = 4 THEN NULL ELSE sub END, CASE WHEN ord = 4 THEN sub END DESC;
COMMIT;
