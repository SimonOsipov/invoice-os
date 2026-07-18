-- +goose Up
-- M4-06-03: closes the invoices -> business_entities leg of the D8 cross-tenant
-- residual (per user decision). Today's single-column entity_id FK checks existence
-- only -- its referential-integrity trigger runs with RLS bypassed, so it happily
-- accepts a cross-tenant entity_id (pinned by the now-flipped
-- TestRLS_InvoicesCrossTenantDanglingEntityRef / TestStoreCreate_CrossTenantEntityIDRejected).
-- Replacing it with a composite (tenant_id, entity_id) FK makes the FK's own existence
-- check tenant-scoped: business_entities must carry a matching (tenant_id, id) row, so a
-- cross-tenant entity_id 0-matches and the insert is rejected with 23503.
--
-- business_entities has no unique key covering (tenant_id, id) yet -- id alone is the
-- PK, so a composite FK needs a composite unique/PK on the referenced side first.
-- Adding UNIQUE (tenant_id, id) is redundant with the PK for uniqueness purposes but is
-- exactly what Postgres requires a composite FK to reference.
--
-- The new FK is NOT VALID: it enforces on all NEW writes but does not scan/validate
-- existing rows. That is deliberate, not just an optimization -- it is what makes this
-- migration safe to apply on the shared dev migrator (which may carry rows written by
-- other in-flight stories) and on this local DB (which may still carry cross-tenant rows
-- the RED tests in M4-06-03 left behind before this migration ran). A VALIDATE CONSTRAINT
-- backfill is intentionally NOT run here -- no consumer needs historical rows enforced,
-- only new writes.
--
-- ON DELETE RESTRICT is carried over unchanged from the FK it replaces (invoices are
-- durable legal/fiscal records, D9 -- see 20260714103137_invoices.sql's header).
--
-- Sibling residuals are INTENTIONALLY left untouched by this migration: line_items ->
-- invoices, invoice_status_history -> invoices, and invoices -> import_batches all stay
-- single-column, accepted D8 residuals (pinned by their own dangling-ref RLS specs). Only
-- the invoices -> business_entities leg is in scope for M4-06-03.
ALTER TABLE business_entities
    ADD CONSTRAINT business_entities_tenant_id_id_uq UNIQUE (tenant_id, id);
ALTER TABLE invoices
    DROP CONSTRAINT invoices_entity_id_fkey;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_tenant_entity_fk
        FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE RESTRICT NOT VALID;

-- +goose Down
ALTER TABLE invoices
    DROP CONSTRAINT invoices_tenant_entity_fk;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_entity_id_fkey
        FOREIGN KEY (entity_id) REFERENCES business_entities (id) ON DELETE RESTRICT;
ALTER TABLE business_entities
    DROP CONSTRAINT business_entities_tenant_id_id_uq;
