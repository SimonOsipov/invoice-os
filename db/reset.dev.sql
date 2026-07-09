-- db/reset.dev.sql — DATA-ONLY reset of the dev tenant fixtures (M2-14). NOT a migration.
--
-- The topology-e2e workflow runs this then db/seed.dev.sql against the DEPLOYED dev
-- Postgres, so every run starts from a known fixture set regardless of prior state —
-- setup owns correctness, not teardown (teardown isn't guaranteed to run). Kept separate
-- from seed.dev.sql so the seed rows live in exactly one place (reset here, insert there).
--
-- MUST run as the Postgres SUPERUSER (the deployed dev DATABASE_PUBLIC_URL): `tenants` is
-- FORCE ROW LEVEL SECURITY, so even its owner (invoice_migrator) is subject to the
-- isolation policy and TRUNCATE would be blocked — only the superuser (BYPASSRLS) can
-- clear it. Mirrors how local `make dev-db` seeds as the in-container superuser.
--
-- DATA-ONLY: this TRUNCATEs a table's ROWS. Schema and migration history are untouched
-- and persist (the dev Postgres is always-on). CASCADE covers any future tenant-scoped
-- child tables (FKs to tenants); today `tenants` is the only fixture table. Idempotent —
-- safe to re-run (TRUNCATE on an empty table is a no-op).

TRUNCATE tenants CASCADE;
