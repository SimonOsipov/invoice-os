-- M3-01-03: business_entities — the tenant-owned portfolio table (a firm's 20–200
-- client businesses; docs/migrations.md §3, §8).
--
-- This table is born with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (ENABLE + FORCE ROW LEVEL SECURITY, a permissive USING that doubles as the
-- INSERT/UPDATE WITH CHECK, tenant_id = the app.current_tenant GUC, fail-closed when
-- unset) — same shape as tenants/audit_log/idempotency_keys.
--
-- Deliberately NO `tenant_enumerate`/invoice_tenant_reader policy — matches
-- audit_log/idempotency_keys, not tenants. Least-privilege per docs/migrations.md §3:
-- nothing outside a tenant's own portfolio has a documented cross-tenant read need yet
-- (QA-Verify F1 conservative-default — grant reader access later, in the migration
-- that introduces the actual need, not preemptively here).
--
-- FK to tenants(id) ON DELETE CASCADE: business_entities are genuinely owned by a
-- tenant row and should not be able to outlive it, so this departs from the bare-uuid
-- audit_log/idempotency_keys precedent by design — referential integrity here matters
-- more than keeping the hot path free of a cross-table lock (this is not a
-- high-frequency append path like those two).
--
-- tenant_id has NO GUC default (unlike audit_log): it is a plain caller-supplied
-- NOT NULL column, not an implicit-actor ledger — the caller always knows which
-- tenant's portfolio it is inserting into.
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE business_entities (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         text        NOT NULL,
    tin          text,
    registration text,
    sector       text,
    address      text,
    status       text        NOT NULL DEFAULT 'active'
                             CHECK (status IN ('active', 'archived')),
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policy below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE business_entities ENABLE ROW LEVEL SECURITY;
ALTER TABLE business_entities FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees
-- (and can insert/update) a business_entities row only when app.current_tenant equals
-- its tenant_id; an unset GUC → NULL → no rows. This USING doubles as the
-- INSERT/UPDATE WITH CHECK, so both a cross-tenant INSERT and a same-row
-- reassignment to another tenant are refused (42501).
CREATE POLICY tenant_isolation ON business_entities
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

CREATE INDEX business_entities_tenant_id_idx ON business_entities (tenant_id);
CREATE UNIQUE INDEX business_entities_tenant_tin_uq
    ON business_entities (tenant_id, tin) WHERE tin IS NOT NULL;

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating
-- migration, never blanket). invoice_app: full DML — this is the portfolio CRUD
-- surface the app owns. No grant to invoice_tenant_reader (see header).
GRANT SELECT, INSERT, UPDATE, DELETE ON business_entities TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, indexes, and grants with it, so reset→up
-- round-trips clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE business_entities;
