-- +goose Up
CREATE TABLE documents (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id             uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    storage_key           text        NOT NULL,
    content_hash          text        NOT NULL CHECK (char_length(content_hash) = 64),
    size_bytes            bigint      NOT NULL
                                      CHECK (size_bytes >= 0 AND size_bytes <= 15728640),
    filename              text,
    declared_content_type text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    -- Sole purpose: the target of the composite pointer FKs below. A composite FK can
    -- reference a CONSTRAINT only, never a bare unique index
    -- (TestRLS_DocumentsTenantIdIdUniqueConstraintExists).
    CONSTRAINT documents_tenant_id_id_uq UNIQUE (tenant_id, id)
);

-- Force is what subjects the table owner to the policy; enable alone would not
-- (TestRLS_DocumentsOwnerInsertRefusedUnderForce).
ALTER TABLE documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE documents FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON documents
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- The TO clause is load-bearing: permissive policies combine with OR, so an unscoped
-- USING (true) would hand every tenant's rows to invoice_app as well
-- (TestRLS_DocumentsAppUnaffectedByEnumeratePolicy).
CREATE POLICY tenant_enumerate ON documents
    FOR SELECT TO invoice_tenant_reader
    USING (true);

-- Leading with tenant_id keeps dedupe per-tenant; a (content_hash)-only index would make
-- it cross-tenant (TestRLS_DocumentsSameHashDifferentTenantsAllowed).
CREATE UNIQUE INDEX documents_tenant_content_hash_uq ON documents (tenant_id, content_hash);

-- Append-only by grant: no UPDATE, no DELETE for either role. invoice_tenant_reader gets
-- SELECT and no object-storage credential, so it enumerates pointers and fetches no bytes.
GRANT SELECT, INSERT ON documents TO invoice_app;
GRANT SELECT          ON documents TO invoice_tenant_reader;

-- Composite, not single-column: referential-integrity checks run with RLS bypassed, so
-- FOREIGN KEY (document_id) REFERENCES documents(id) would accept another tenant's row.
-- Both columns are entirely NULL here, so validation is free — no NOT VALID split needed.
ALTER TABLE import_batches ADD COLUMN document_id uuid;
ALTER TABLE import_batches
    ADD CONSTRAINT import_batches_tenant_document_fk
        FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, id) ON DELETE RESTRICT;
CREATE INDEX import_batches_document_id_idx
    ON import_batches (document_id) WHERE document_id IS NOT NULL;

-- invoices.import_batch_id is ON DELETE SET NULL, so a batch-only pointer would leave the
-- evidence severable (TestRLS_InvoicesSourceDocumentSurvivesBatchNulling).
ALTER TABLE invoices ADD COLUMN source_document_id uuid;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_tenant_source_document_fk
        FOREIGN KEY (tenant_id, source_document_id)
        REFERENCES documents (tenant_id, id) ON DELETE RESTRICT;
CREATE INDEX invoices_source_document_id_idx
    ON invoices (source_document_id) WHERE source_document_id IS NOT NULL;

-- +goose Down
-- Columns first: dropping them takes their FKs and partial indexes with them, which is
-- what unblocks the table drop.
ALTER TABLE invoices       DROP COLUMN source_document_id;
ALTER TABLE import_batches DROP COLUMN document_id;
DROP TABLE documents;
