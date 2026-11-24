-- +goose Up
CREATE TABLE erp_writebacks (
    tenant_id          uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id                 uuid        NOT NULL DEFAULT gen_random_uuid(),
    entity_id          uuid        NOT NULL,
    connection_id      uuid        NOT NULL,
    source_document_id uuid        NOT NULL,
    invoice_id         uuid        NOT NULL,
    result_version     integer     NOT NULL,
    outcome            text        NOT NULL,
    attempt            integer     NOT NULL,
    state              text        NOT NULL,
    reason             text,
    written_revision   text,
    new_revision       text,
    river_job_id       bigint      NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT erp_writebacks_pk PRIMARY KEY (id),
    CONSTRAINT erp_writebacks_result_version_check CHECK (result_version > 0),
    CONSTRAINT erp_writebacks_attempt_check CHECK (attempt > 0),
    CONSTRAINT erp_writebacks_outcome_check CHECK (outcome IN ('accepted', 'rejected', 'failed')),
    CONSTRAINT erp_writebacks_state_check CHECK (state IN ('succeeded', 'failed', 'skipped')),
    CONSTRAINT erp_writebacks_reason_check CHECK (
        (state = 'succeeded' AND reason IS NULL)
        OR (state = 'failed' AND reason IS NOT NULL AND reason IN ('not_found', 'unsupported', 'revision_conflict', 'rate_limited', 'vendor'))
        OR (state = 'skipped' AND reason IS NOT NULL AND reason IN ('disconnected', 'nothing_writable', 'superseded', 'deleted_in_erp'))),
    -- Composite: RI checks bypass RLS.
    CONSTRAINT erp_writebacks_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT erp_writebacks_tenant_connection_fk FOREIGN KEY (tenant_id, connection_id)
        REFERENCES erp_connections (tenant_id, id),
    CONSTRAINT erp_writebacks_tenant_document_fk FOREIGN KEY (tenant_id, source_document_id)
        REFERENCES erp_source_documents (tenant_id, id),
    CONSTRAINT erp_writebacks_tenant_invoice_fk FOREIGN KEY (tenant_id, invoice_id)
        REFERENCES invoices (tenant_id, id) ON DELETE RESTRICT
);
-- The retry's done-check: one document's attempts for one result.
CREATE INDEX erp_writebacks_document_idx ON erp_writebacks (tenant_id, source_document_id, result_version);
ALTER TABLE erp_writebacks ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp_writebacks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON erp_writebacks USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));
-- An append-only attempt log: no UPDATE, no DELETE.
GRANT SELECT, INSERT ON erp_writebacks TO invoice_app;

-- +goose Down
DROP TABLE erp_writebacks;
