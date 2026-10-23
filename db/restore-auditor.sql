-- One-time, as postgres: the NOLOGIN BYPASSRLS role db/restore-check.sql runs under on Cloud SQL,
-- where postgres itself does not bypass RLS. Enter it with SET ROLE restore_auditor, then run the check.
-- Idempotent. It reads only what restore-check.sql reads.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'restore_auditor') THEN
    CREATE ROLE restore_auditor NOLOGIN BYPASSRLS;
  END IF;
END
$$;
ALTER ROLE restore_auditor WITH NOLOGIN BYPASSRLS NOCREATEDB NOCREATEROLE;
GRANT USAGE ON SCHEMA public TO restore_auditor;
-- Cloud SQL's postgres neither owns the tables nor is a superuser: it grants as the owner, through the
-- ADMIN it holds on that role (db/bootstrap.sql grants supabase_auth_admin the same way).
DO $$
DECLARE
  v_owner text := (SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = 'public.tenants'::regclass);
BEGIN
  IF NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) AND v_owner <> current_user THEN
    EXECUTE format('GRANT %I TO CURRENT_USER WITH INHERIT FALSE, SET TRUE', v_owner);
    EXECUTE format('SET ROLE %I', v_owner);
  END IF;
END
$$;
GRANT SELECT ON tenants, invoices, line_items, documents, audit_log, memberships, goose_db_version TO restore_auditor;
RESET ROLE;
GRANT restore_auditor TO CURRENT_USER WITH INHERIT FALSE, SET TRUE;
