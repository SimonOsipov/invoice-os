-- M4-01-02: invoices — the canonical invoice, on FiscalBridge's own terms (NOT a
-- mirror of the MBS/FIRS wire format the validation engine consumes; that mapping is
-- M4-04's job). This is the spine the import -> validate -> fix -> re-validate loop
-- writes into.
--
-- This table is born with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (ENABLE + FORCE ROW LEVEL SECURITY, a permissive USING that doubles as the
-- INSERT/UPDATE WITH CHECK, tenant_id = the app.current_tenant GUC, fail-closed when
-- unset) — same shape as tenants/audit_log/idempotency_keys/business_entities/
-- import_batches.
--
-- Deliberately NO `tenant_enumerate`/invoice_tenant_reader policy — no cross-tenant
-- consumer needs to enumerate invoices across tenants (Simon Vault "M4-01 Invoice
-- Spine Migrations" §System Design, disposition F1 carried forward from M3-01).
--
-- Store-invalid-faithfully (the load-bearing design principle, story §System Design):
-- schema CHECK/NOT-NULL constraints live only on SYSTEM-controlled structural columns
-- (id, tenant_id, entity_id, invoice_number, status, created_at). The MBS-content
-- columns imported from a CSV (issue_date, supplier_tin/name, buyer_tin/name,
-- currency, subtotal/vat/total) are NULLABLE and carry NO CHECK — a CHECK here would
-- hard-reject exactly the rows the M4-04 validate gate must be able to store and later
-- report violations for (e.g. a negative subtotal or a NULL currency is storable, not
-- rejected at the schema).
--
-- FK dispositions (durability, D9): entity_id -> business_entities(id) is
-- ON DELETE RESTRICT, NOT CASCADE like import_batches.entity_id — an invoice is a
-- durable legal/fiscal record (possibly accepted/FIRS-submitted) and must not be
-- silently destroyed by a business_entities hard delete. import_batch_id ->
-- import_batches(id) is ON DELETE SET NULL — the invoice outlives the disposable
-- import-run record. rule_set_version_id -> rule_set_versions(id) has NO ON DELETE
-- clause (NO ACTION) — mirrors the memberships.role -> roles(name) global-lookup
-- pattern.
--
-- invoice_number is NOT NULL (identity, not validatable content — a numberless import
-- row has no identity and is quarantined by M4-03, never stored here), so the unique
-- guard below is a plain (non-partial) UNIQUE across all states.
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE invoices (
    id                  uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid          NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    entity_id           uuid          NOT NULL REFERENCES business_entities(id) ON DELETE RESTRICT,
    import_batch_id     uuid          REFERENCES import_batches(id) ON DELETE SET NULL,
    invoice_number      text          NOT NULL,
    status              text          NOT NULL DEFAULT 'draft'
                                      CHECK (status IN ('draft', 'validated', 'queued',
                                                        'submitted', 'accepted', 'rejected', 'failed')),
    -- MBS-content: NULLABLE, no CHECK (store-invalid — see header).
    issue_date          date,
    supplier_tin        text,
    supplier_name       text,
    buyer_tin           text,
    buyer_name          text,
    currency            text,
    subtotal            numeric(14,2),
    vat                 numeric(14,2),
    total               numeric(14,2),
    -- Validation results, populated by M4-04.
    violations          jsonb         NOT NULL DEFAULT '[]',
    rule_set_version_id uuid          REFERENCES rule_set_versions(id),
    created_at          timestamptz   NOT NULL DEFAULT now()
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policy below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE invoices FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees
-- (and can insert/update) an invoices row only when app.current_tenant equals its
-- tenant_id; an unset GUC → NULL → no rows. This USING doubles as the INSERT/UPDATE
-- WITH CHECK, so both a cross-tenant INSERT and a same-row reassignment to another
-- tenant are refused (42501).
CREATE POLICY tenant_isolation ON invoices
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- The hard duplicate-detection guard (AC-3) — also leads with tenant_id, so no
-- standalone tenant_id index is added (would be redundant, D12).
CREATE UNIQUE INDEX invoices_tenant_entity_number_uq
    ON invoices (tenant_id, entity_id, invoice_number);

-- Per-entity state rollups (M4-07) + services the entity_id RESTRICT-delete FK check.
CREATE INDEX invoices_entity_status_idx ON invoices (entity_id, status);

-- Batch membership (M4-03/M4-06 within-file duplicate detection); partial because
-- import_batch_id is nullable and most lookups only care about batch-linked rows.
CREATE INDEX invoices_import_batch_id_idx ON invoices (import_batch_id) WHERE import_batch_id IS NOT NULL;

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating
-- migration, never blanket). invoice_app: the fix loop (M4-05) edits rows in place —
-- no DELETE (no hard-delete consumer exists yet). No grant to invoice_tenant_reader
-- (see header).
GRANT SELECT, INSERT, UPDATE ON invoices TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, indexes, constraints, and grants with it, so
-- reset→up round-trips clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE invoices;
