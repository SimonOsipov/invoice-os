-- db/bootstrap.sql — one-time, superuser-run role bootstrap for the FiscalBridge DB.
--
-- Creates the two NON-superuser LOGIN roles the whole M2 data model depends on:
--   invoice_migrator — owns every schema object and runs migrations (goose).
--   invoice_app      — the runtime connection identity for the services.
--
-- Why non-superuser + NOBYPASSRLS matters: Row-Level Security is only *enforceable*
-- if the roles it applies to cannot bypass it and do not own the tables (an owner
-- bypasses RLS unless the table is FORCE'd — proven adversarially in M2-07). So the
-- migrator (table owner) and the app (query identity) are deliberately distinct, and
-- neither is the Railway superuser. See docs/migrations.md.
--
-- Run ONCE as the Postgres superuser, via psql (uses \set and :'var' interpolation):
--   psql "$DATABASE_SUPERUSER_URL" -v ON_ERROR_STOP=1 \
--     -v migrator_password="…" -v app_password="…" -f db/bootstrap.sql
-- `make db-bootstrap` does exactly this with dev-default passwords. Idempotent:
-- safe to re-run (it also rotates the passwords and re-asserts the attributes).

\set ON_ERROR_STOP on

-- 1. Create the roles if they don't exist. PG has no CREATE ROLE IF NOT EXISTS, and
--    psql does NOT interpolate :'var' inside a dollar-quoted block — so create here
--    without a password, then set passwords at top level (step 3).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_migrator') THEN
    CREATE ROLE invoice_migrator LOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_app') THEN
    CREATE ROLE invoice_app LOGIN;
  END IF;
END
$$;

-- 2. Re-assert the security attributes on every run (fixes drift if a role
--    pre-existed with different attributes). NOBYPASSRLS is the load-bearing one.
ALTER ROLE invoice_migrator WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
ALTER ROLE invoice_app      WITH LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;

-- 3. Set / rotate passwords (top level so psql :'var' interpolation applies).
ALTER ROLE invoice_migrator PASSWORD :'migrator_password';
ALTER ROLE invoice_app      PASSWORD :'app_password';

-- 4. Grants.
--    Migrator owns the objects it creates → needs CREATE + USAGE on the schema.
GRANT USAGE, CREATE ON SCHEMA public TO invoice_migrator;

--    Lock down the public schema: revoke the ambient CREATE that PUBLIC has by
--    default (a no-op on PG15+, where PUBLIC already lacks it — kept explicit for
--    PG13/14 and as defense-in-depth).
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

--    invoice_app gets NO grants here. Per the least-privilege convention, every
--    privilege the app role needs on a table (SELECT/INSERT/…) is granted
--    EXPLICITLY in the migration that creates that table — never blanket-granted
--    and never via ALTER DEFAULT PRIVILEGES. The skeleton migration creates no
--    tables, so the app role correctly has nothing to touch yet. See docs/migrations.md.
