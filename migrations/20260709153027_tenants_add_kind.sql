-- M3-01-02: the `tenants.kind` discriminator — firm vs in-house (task-25).
--
-- Distinguishes an accounting firm tenant ('firm') from an in-house corporate tenant
-- ('in_house'); downstream M3-01 subtasks branch behavior on this. `DEFAULT 'firm'` is
-- load-bearing, not cosmetic: it backfills every pre-existing row in one statement, so
-- the NOT NULL add is safe on the already-populated table, and it keeps every committed
-- `INSERT INTO tenants (id, name)` site working unchanged — db/seed.dev.sql and the
-- M2-07 RLS harness both insert tenants by (id, name) only. The DEFAULT stays (no
-- `DROP DEFAULT` after backfill): those call sites still don't name `kind`. text+CHECK
-- (not an enum type) keeps this a single, reversible ALTER — no separate CREATE TYPE /
-- DROP TYPE to sequence around goose's per-migration transaction.

-- +goose Up
ALTER TABLE tenants
    ADD COLUMN kind text NOT NULL DEFAULT 'firm'
        CHECK (kind IN ('firm', 'in_house'));

-- +goose Down
ALTER TABLE tenants
    DROP COLUMN kind;
