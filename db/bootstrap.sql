-- db/bootstrap.sql — one-time, superuser-run role bootstrap for the FiscalBridge DB.
--
-- Creates the NON-superuser LOGIN roles the whole M2 data model depends on:
--   invoice_migrator       — owns every schema object and runs migrations (goose).
--   invoice_app            — the runtime connection identity for the services.
--   invoice_tenant_reader  — the ONE cross-tenant enumeration identity (M2-06). It
--     exists so ops/reconciliation code (M5-06) can list ALL tenants without anyone
--     getting BYPASSRLS: a `FOR SELECT TO invoice_tenant_reader USING (true)` policy
--     on `tenants` (added by the M2-06 migration) lets this role — and only this role
--     — see every tenant row, while invoice_app still sees only its current tenant.
--     Still NOBYPASSRLS: its reach is exactly what its policies+grants allow, no more.
--
-- Why non-superuser + NOBYPASSRLS matters: Row-Level Security is only *enforceable*
-- if the roles it applies to cannot bypass it and do not own the tables (an owner
-- bypasses RLS unless the table is FORCE'd — proven adversarially in M2-07). So the
-- migrator (table owner) and the app (query identity) are deliberately distinct, and
-- neither is the Railway superuser. See docs/migrations.md.
--
-- Run ONCE as the Postgres superuser. Password values come from three session GUCs
-- the caller sets on the SAME connection before this file runs — no psql-only
-- meta-commands or client-side interpolation, so both psql and a pgx caller (the
-- migration runner, M4-21-03) execute this identical file (Decision
-- [one-bootstrap-file]):
--   psql "$DATABASE_SUPERUSER_URL" -v ON_ERROR_STOP=1 \
--     -c "SELECT set_config('fiscalbridge.migrator_password', '…', false)" \
--     -c "SELECT set_config('fiscalbridge.app_password',      '…', false)" \
--     -c "SELECT set_config('fiscalbridge.reader_password',   '…', false)" \
--     -f db/bootstrap.sql
-- `make db-bootstrap` / `make dev-db` do exactly this with dev-default passwords.
-- Idempotent: safe to re-run (it also rotates the passwords and re-asserts the
-- attributes). Fails closed: a missing/empty GUC raises before any password is
-- touched (step 3 below).

-- 1. Create the roles if they don't exist. PG has no CREATE ROLE IF NOT EXISTS, and
--    CREATE ROLE ... PASSWORD does not accept a bind parameter/GUC directly — so
--    create here without a password, then set passwords at step 3 via
--    EXECUTE format(...).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_migrator') THEN
    CREATE ROLE invoice_migrator LOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_app') THEN
    CREATE ROLE invoice_app LOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_tenant_reader') THEN
    CREATE ROLE invoice_tenant_reader LOGIN;
  END IF;
END
$$;

-- 2. Re-assert the security attributes on every run (fixes drift if a role
--    pre-existed with different attributes). NOBYPASSRLS is the load-bearing one —
--    it applies to invoice_tenant_reader too: that role sees all tenants only because
--    a policy grants it, never by bypassing RLS.
ALTER ROLE invoice_migrator      WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
ALTER ROLE invoice_app           WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
ALTER ROLE invoice_tenant_reader WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;

-- 3. Set / rotate passwords, read from three session GUCs the caller sets beforehand
--    (`fiscalbridge.migrator_password` / `.app_password` / `.reader_password`) —
--    replaces psql's :'var' client-side interpolation, which a pgx caller has no
--    equivalent of. Fail closed: every GUC is checked BEFORE any password is
--    applied, so a partial run never rotates one role while leaving another's GUC
--    missing. current_setting(name, true)'s `true` = missing_ok, so an unset GUC
--    reads NULL (coalesced to '' below) instead of erroring here — the explicit
--    RAISE is what turns that into a loud failure. An explicitly-set empty string
--    is treated identically to unset (both coalesce to ''): neither is ever a valid
--    password, so both fail closed the same way. CREATE/ALTER ROLE ... PASSWORD
--    does not accept a bind parameter, which is why this needs EXECUTE at all;
--    %L (literal quoting) is what makes that EXECUTE format(...) injection-safe.
DO $$
BEGIN
  IF coalesce(current_setting('fiscalbridge.migrator_password', true), '') = '' THEN
    RAISE EXCEPTION 'fiscalbridge.migrator_password is not set (or empty) — set it via SELECT set_config(...) on this session before running db/bootstrap.sql';
  END IF;
  IF coalesce(current_setting('fiscalbridge.app_password', true), '') = '' THEN
    RAISE EXCEPTION 'fiscalbridge.app_password is not set (or empty) — set it via SELECT set_config(...) on this session before running db/bootstrap.sql';
  END IF;
  IF coalesce(current_setting('fiscalbridge.reader_password', true), '') = '' THEN
    RAISE EXCEPTION 'fiscalbridge.reader_password is not set (or empty) — set it via SELECT set_config(...) on this session before running db/bootstrap.sql';
  END IF;

  EXECUTE format('ALTER ROLE invoice_migrator      PASSWORD %L', current_setting('fiscalbridge.migrator_password'));
  EXECUTE format('ALTER ROLE invoice_app           PASSWORD %L', current_setting('fiscalbridge.app_password'));
  EXECUTE format('ALTER ROLE invoice_tenant_reader PASSWORD %L', current_setting('fiscalbridge.reader_password'));
END
$$;

-- 4. Grants.
--    Migrator owns the objects it creates → needs CREATE + USAGE on the schema.
GRANT USAGE, CREATE ON SCHEMA public TO invoice_migrator;

--    invoice_tenant_reader needs USAGE on the schema to reach any table it is later
--    granted SELECT on (that per-table SELECT — e.g. on `tenants` — is granted in the
--    migration, per the convention below). USAGE alone touches no data.
GRANT USAGE ON SCHEMA public TO invoice_tenant_reader;

--    Lock down the public schema: revoke the ambient CREATE that PUBLIC has by
--    default (a no-op on PG15+, where PUBLIC already lacks it — kept explicit for
--    PG13/14 and as defense-in-depth).
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

--    Neither invoice_app nor invoice_tenant_reader gets any TABLE privilege here. Per
--    the least-privilege convention, every privilege a role needs on a table
--    (SELECT/INSERT/…) is granted EXPLICITLY in the migration that creates that table
--    — never blanket-granted and never via ALTER DEFAULT PRIVILEGES. The skeleton
--    migration creates no tables; the M2-06 migration creates `tenants` and grants
--    both roles SELECT on it there. See docs/migrations.md §3.
