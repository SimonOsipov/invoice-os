-- import_mappings: the column mapping a client's latest completed spreadsheet import used,
-- one row per (tenant, entity, column signature).

-- +goose Up
CREATE TABLE import_mappings (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    entity_id        uuid        NOT NULL,
    -- Go derives it (internal/importer/saved_mapping.go columnSignature).
    column_signature text        NOT NULL CHECK (column_signature ~ '^[0-9a-f]{64}$'),
    mapping          jsonb       NOT NULL CHECK (jsonb_typeof(mapping) = 'object'
                                            AND mapping ? 'invoice_number'),
    saved_at         timestamptz NOT NULL DEFAULT now(),
    -- Composite so a cross-tenant entity_id 0-matches; FK checks run RLS-bypassed.
    CONSTRAINT import_mappings_tenant_entity_fk
        FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT import_mappings_tenant_entity_signature_uq
        UNIQUE (tenant_id, entity_id, column_signature)
);

ALTER TABLE import_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_mappings FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON import_mappings
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- No DELETE: only the entity's cascade removes a row. No reader grant.
GRANT SELECT, INSERT, UPDATE ON import_mappings TO invoice_app;

-- +goose Down
DROP TABLE import_mappings;
