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
GRANT SELECT ON tenants, invoices, line_items, documents, audit_log, memberships, goose_db_version TO restore_auditor;
GRANT restore_auditor TO CURRENT_USER WITH INHERIT FALSE, SET TRUE;
