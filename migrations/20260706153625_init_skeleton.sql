-- Skeleton / baseline migration (M2-01).
--
-- Intentionally a no-op. Its only job is to prove the pipeline end-to-end:
-- goose connects as the migrator role, creates its `goose_db_version` ledger,
-- records this version on `up`, and removes it on `down`. Applying it exercises
-- the migrator's CREATE privilege on the schema (granted by db/bootstrap.sql)
-- without introducing any domain object.
--
-- Deliberately NO DDL here:
--   * app.current_tenant is a runtime GUC (current_setting/SET LOCAL) — needs no schema.
--   * gen_random_uuid() is built in on PG13+ (pgcrypto folded into core) — no extension.
--
-- The first real table (`tenants`) and its FORCE RLS policy are M2-06; the
-- adversarial RLS proof is M2-07. See docs/migrations.md.

-- +goose Up

-- +goose Down
