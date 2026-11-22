-- +goose Up
CREATE TABLE erp_source_documents (
    tenant_id                  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id                         uuid        NOT NULL DEFAULT gen_random_uuid(),
    entity_id                  uuid        NOT NULL,
    connection_id              uuid        NOT NULL,
    source_doc_id              text        NOT NULL,
    source_revision            text        NOT NULL,
    content_hash               text,
    hash_version               smallint,
    invoice_id                 uuid,
    state                      text        NOT NULL DEFAULT 'pending',
    failure                    text,
    claimed_number             text,
    differs_from_erp           boolean     NOT NULL DEFAULT false,
    changed_after_clearance_at timestamptz,
    deleted_in_erp_at          timestamptz,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT erp_source_documents_pk PRIMARY KEY (id),
    CONSTRAINT erp_source_documents_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT erp_source_documents_connection_doc_uq UNIQUE (connection_id, source_doc_id),
    CONSTRAINT erp_source_documents_connection_invoice_uq UNIQUE (connection_id, invoice_id),
    CONSTRAINT erp_source_documents_state_check
        CHECK (state IN ('pending', 'drafted', 'linked', 'held', 'failed', 'deleted')),
    CONSTRAINT erp_source_documents_invoice_iff_taken
        CHECK ((state IN ('drafted', 'linked')) = (invoice_id IS NOT NULL)),
    CONSTRAINT erp_source_documents_failure_check
        CHECK ((state = 'failed') = (failure IS NOT NULL)
               AND (failure IS NULL OR failure IN ('unmappable', 'refused', 'not_found', 'no_mapping', 'exhausted'))),
    CONSTRAINT erp_source_documents_claim_check
        CHECK (claimed_number IS NULL OR state = 'pending'),
    CONSTRAINT erp_source_documents_hash_check
        CHECK (content_hash IS NULL OR content_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT erp_source_documents_hash_version_check
        CHECK ((content_hash IS NULL) = (hash_version IS NULL) AND (hash_version IS NULL OR hash_version > 0)),
    -- Composite: RI checks bypass RLS.
    CONSTRAINT erp_source_documents_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT erp_source_documents_tenant_connection_fk FOREIGN KEY (tenant_id, connection_id)
        REFERENCES erp_connections (tenant_id, id),
    CONSTRAINT erp_source_documents_tenant_invoice_fk FOREIGN KEY (tenant_id, invoice_id)
        REFERENCES invoices (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX erp_source_documents_invoice_idx ON erp_source_documents (tenant_id, invoice_id) WHERE invoice_id IS NOT NULL;
-- The rival-claim check of the ingest: one client's live claims on one number.
CREATE INDEX erp_source_documents_claim_idx ON erp_source_documents (entity_id, claimed_number) WHERE claimed_number IS NOT NULL;
ALTER TABLE erp_source_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp_source_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON erp_source_documents USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));
-- Documents are kept: no DELETE.
GRANT SELECT, INSERT, UPDATE ON erp_source_documents TO invoice_app;

ALTER TABLE invoices ADD COLUMN erp_connection_id uuid;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_tenant_erp_connection_fk
        FOREIGN KEY (tenant_id, erp_connection_id)
        REFERENCES erp_connections (tenant_id, id) ON DELETE RESTRICT;
CREATE INDEX invoices_erp_connection_id_idx
    ON invoices (erp_connection_id) WHERE erp_connection_id IS NOT NULL;

ALTER TABLE erp_connections ADD COLUMN ingest_epoch bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE erp_connections DROP COLUMN ingest_epoch;
ALTER TABLE invoices DROP COLUMN erp_connection_id;
DROP TABLE erp_source_documents;
