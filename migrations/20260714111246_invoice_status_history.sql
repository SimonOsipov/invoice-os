-- M4-01-04: invoice_status_history — one append-only row per invoice state
-- transition. FK-attached to both its tenant (for cascade/RLS) and its parent
-- invoice (history is an inseparable part of the invoice it belongs to).
--
-- This table is born with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (ENABLE + FORCE ROW LEVEL SECURITY, a permissive USING that doubles as the
-- INSERT WITH CHECK, tenant_id = the app.current_tenant GUC, fail-closed when
-- unset) — same shape as tenants/audit_log/idempotency_keys/business_entities/
-- import_batches/invoices/line_items.
--
-- Deliberately NO `tenant_enumerate`/invoice_tenant_reader policy — matches every
-- other M4-01 table: no cross-tenant consumer needs to enumerate status history
-- across tenants (Simon Vault "M4-01 Invoice Spine Migrations" §System Design,
-- disposition D11 / F1 carried forward from M3-01).
--
-- APPEND-ONLY (D10): enforced by the GRANT (SELECT, INSERT to invoice_app only —
-- no UPDATE, no DELETE), the idempotency_keys precedent. Deliberately NO
-- owner-proof trigger — that extra hardening belongs to audit_log (the
-- tamper-evidence trail M4-02 also writes to per transition, out of scope here),
-- not this operational state log.
--
-- from_status is nullable (CHECK enum-or-null) — the very first transition
-- (NULL -> 'draft') has no predecessor state. to_status is NOT NULL (CHECK the
-- 7-state set) — every transition lands somewhere. actor is NOT NULL with a
-- char_length > 0 CHECK — a GoTrue subject uuid or 'system' (the audit_log.actor
-- precedent). All four columns here are SYSTEM-written (never imported CSV
-- content, M4-02 owns every write), so CHECKs are free integrity under the
-- store-invalid-faithfully principle (D2) — unlike the MBS-content columns on
-- invoices/line_items.
--
-- invoice_id is ON DELETE CASCADE — history is an inseparable part of its
-- invoice (the invoice itself is protected by its own entity_id RESTRICT, D9);
-- tenant_id is ON DELETE CASCADE on the parent tenants row per the M3-01
-- child-table precedent.
--
-- FK dispositions (D8): tenant-owned -> tenant-owned FKs (invoice_id) are plain
-- per-column FKs, not composite same-tenant FKs — the accepted cross-tenant
-- dangling-reference residual is asserted, not defended, by ISH-RLS-13.
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE invoice_status_history (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invoice_id  uuid        NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    from_status text        CHECK (from_status IS NULL OR from_status IN
                              ('draft', 'validated', 'queued', 'submitted',
                               'accepted', 'rejected', 'failed')),
    to_status   text        NOT NULL CHECK (to_status IN
                              ('draft', 'validated', 'queued', 'submitted',
                               'accepted', 'rejected', 'failed')),
    actor       text        NOT NULL CHECK (char_length(actor) > 0),
    changed_at  timestamptz NOT NULL DEFAULT now()
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policy below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE invoice_status_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoice_status_history FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees
-- (and can insert) an invoice_status_history row only when app.current_tenant
-- equals its tenant_id; an unset GUC → NULL → no rows. This USING doubles as the
-- INSERT WITH CHECK, so a cross-tenant INSERT is refused (42501). There is no
-- UPDATE grant at all (see below), so same-row reassignment is not a distinct
-- case here — every UPDATE is refused at the grant layer before RLS runs.
CREATE POLICY tenant_isolation ON invoice_status_history
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Lookup (per-invoice history) + invoice-cascade FK index.
CREATE INDEX invoice_status_history_invoice_id_idx ON invoice_status_history (invoice_id);

-- Tenant-cascade FK index.
CREATE INDEX invoice_status_history_tenant_id_idx ON invoice_status_history (tenant_id);

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating
-- migration, never blanket). APPEND-ONLY: invoice_app gets SELECT + INSERT only
-- — no UPDATE, no DELETE (the idempotency_keys precedent, D10/D11). No grant to
-- invoice_tenant_reader (see header).
GRANT SELECT, INSERT ON invoice_status_history TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, indexes, constraints, and grants with it, so
-- reset→up round-trips clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE invoice_status_history;
