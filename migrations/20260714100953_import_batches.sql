-- M4-01-01: import_batches — the tenant-owned bulk-import-run record (a minimal
-- row M4-03's parser writes into; parser logic itself is out of scope here).
--
-- This table is born with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (ENABLE + FORCE ROW LEVEL SECURITY, a permissive USING that doubles as the
-- INSERT/UPDATE WITH CHECK, tenant_id = the app.current_tenant GUC, fail-closed when
-- unset) — same shape as tenants/audit_log/idempotency_keys/business_entities.
--
-- Deliberately NO `tenant_enumerate`/invoice_tenant_reader policy — matches
-- business_entities: no cross-tenant consumer needs to enumerate import_batches
-- across tenants (Simon Vault "M4-01 Invoice Spine Migrations" §System Design,
-- QA-Verify F1 conservative-default carried forward from M3-01).
--
-- FK tenant_id -> tenants(id) ON DELETE CASCADE and entity_id ->
-- business_entities(id) ON DELETE CASCADE: a batch is a genuinely disposable
-- import-run record (unlike an invoices row, whose entity_id FK is RESTRICT for
-- durability — see M4-01-02), so both parents cascade it away.
--
-- Row counters (rows_total/rows_valid/rows_invalid) are SYSTEM-written, never
-- imported CSV content, so a non-negative CHECK is free integrity with zero
-- store-invalid risk (the store-invalid-faithfully principle in the story's
-- System Design applies only to MBS-content columns, which this table has none of).
-- Deliberately NO source-filename / completed_at column (minimal; deferred to
-- M4-03/M4-11 if a real consumer needs them).
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE import_batches (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    entity_id    uuid        NOT NULL REFERENCES business_entities(id) ON DELETE CASCADE,
    status       text        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    rows_total   integer     NOT NULL DEFAULT 0,
    rows_valid   integer     NOT NULL DEFAULT 0,
    rows_invalid integer     NOT NULL DEFAULT 0,
    errors       jsonb       NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT import_batches_counts_non_negative
        CHECK (rows_total >= 0 AND rows_valid >= 0 AND rows_invalid >= 0)
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policy below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE import_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_batches FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees
-- (and can insert/update) an import_batches row only when app.current_tenant equals
-- its tenant_id; an unset GUC → NULL → no rows. This USING doubles as the
-- INSERT/UPDATE WITH CHECK, so both a cross-tenant INSERT and a same-row
-- reassignment to another tenant are refused (42501).
CREATE POLICY tenant_isolation ON import_batches
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

CREATE INDEX import_batches_tenant_id_idx ON import_batches (tenant_id);
CREATE INDEX import_batches_entity_id_idx ON import_batches (entity_id);

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating
-- migration, never blanket). invoice_app: created then result-updated by M4-03 —
-- no DELETE (no hard-delete consumer exists yet). No grant to invoice_tenant_reader
-- (see header).
GRANT SELECT, INSERT, UPDATE ON import_batches TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, indexes, constraints, and grants with it,
-- so reset→up round-trips clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE import_batches;
