-- M4-01-03: line_items — an invoice's lines as first-class child rows (not a JSON
-- blob). FK-attached to both its tenant (for cascade/RLS) and its parent invoice
-- (lines are an inseparable part of the invoice they belong to).
--
-- This table is born with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (ENABLE + FORCE ROW LEVEL SECURITY, a permissive USING that doubles as the
-- INSERT/UPDATE WITH CHECK, tenant_id = the app.current_tenant GUC, fail-closed when
-- unset) — same shape as tenants/audit_log/idempotency_keys/business_entities/
-- import_batches/invoices.
--
-- Deliberately NO `tenant_enumerate`/invoice_tenant_reader policy — matches every
-- other M4-01 table: no cross-tenant consumer needs to enumerate line_items across
-- tenants (Simon Vault "M4-01 Invoice Spine Migrations" §System Design, disposition
-- D11 / F1 carried forward from M3-01).
--
-- Store-invalid-faithfully (the load-bearing design principle, story §System Design,
-- D2): schema CHECK/NOT-NULL constraints live only on SYSTEM-controlled structural
-- columns (id, tenant_id, invoice_id, line_no, created_at). The MBS-content columns
-- imported from a CSV (description, quantity, unit_price, line_total, line_tax) are
-- NULLABLE and carry NO CHECK — a CHECK here would hard-reject exactly the rows the
-- M4-04 validate gate must be able to store and later report violations for (e.g. a
-- negative unit_price or a NULL description is storable, not rejected at the schema).
--
-- id is the STABLE line id the MBS `no-duplicate-line-items` CEL rule keys on (M4-04
-- maps it into the payload) — not merely a surrogate key.
--
-- line_no is the system-assigned ordinal (structural, NOT NULL): UNIQUE (invoice_id,
-- line_no) rejects a duplicate ordinal within one invoice (23505) while the same
-- line_no under a DIFFERENT invoice is fine — the guard is per invoice, not global
-- (story D14). This unique index also doubles as the invoice_id-leading cascade
-- index, so no separate invoice_id index is added (D12 no-redundant-index).
--
-- invoice_id is ON DELETE CASCADE — lines are an inseparable part of their invoice
-- (the invoice itself is protected by its own entity_id RESTRICT, D9); tenant_id is
-- ON DELETE CASCADE on the parent tenants row per the M3-01 child-table precedent.
--
-- FK dispositions (D8): tenant-owned -> tenant-owned FKs (invoice_id) are plain
-- per-column FKs, not composite same-tenant FKs — the accepted cross-tenant
-- dangling-reference residual is asserted, not defended, by LI-RLS-12.
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE line_items (
    id          uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid          NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invoice_id  uuid          NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    line_no     integer       NOT NULL,
    -- MBS-content: NULLABLE, no CHECK (store-invalid — see header).
    description text,
    quantity    numeric(14,3),
    unit_price  numeric(14,2),
    line_total  numeric(14,2),
    line_tax    numeric(14,2),
    created_at  timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT line_items_invoice_line_no_uq UNIQUE (invoice_id, line_no)
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policy below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE line_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE line_items FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees
-- (and can insert/update) a line_items row only when app.current_tenant equals its
-- tenant_id; an unset GUC → NULL → no rows. This USING doubles as the INSERT/UPDATE
-- WITH CHECK, so both a cross-tenant INSERT and a same-row reassignment to another
-- tenant are refused (42501).
CREATE POLICY tenant_isolation ON line_items
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Tenant-cascade FK index. The UNIQUE (invoice_id, line_no) constraint above already
-- leads with invoice_id, so no standalone invoice_id index is added (D12).
CREATE INDEX line_items_tenant_id_idx ON line_items (tenant_id);

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating
-- migration, never blanket). invoice_app: the fix loop (M4-05) edits rows in place —
-- no DELETE (no hard-delete consumer exists yet). No grant to invoice_tenant_reader
-- (see header).
GRANT SELECT, INSERT, UPDATE ON line_items TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, indexes, constraints, and grants with it, so
-- reset→up round-trips clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE line_items;
