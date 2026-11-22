-- +goose Up
CREATE TABLE erp_field_mappings (
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id          uuid        NOT NULL DEFAULT gen_random_uuid(),
    entity_id   uuid        NOT NULL,
    system      text        NOT NULL,
    target      text        NOT NULL,
    source_path text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT erp_field_mappings_pk PRIMARY KEY (id),
    CONSTRAINT erp_field_mappings_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT erp_field_mappings_client_system_target_uq UNIQUE (tenant_id, entity_id, system, target),
    CONSTRAINT erp_field_mappings_system_check CHECK (system <> ''),
    CONSTRAINT erp_field_mappings_target_check CHECK (target ~ '^[a-z][a-z0-9_]{0,62}$'),
    CONSTRAINT erp_field_mappings_path_check CHECK (char_length(source_path) BETWEEN 1 AND 256),
    -- Composite: RI checks bypass RLS.
    CONSTRAINT erp_field_mappings_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE erp_field_mappings ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp_field_mappings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON erp_field_mappings USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));
-- DELETE reverts a target to the connector default.
GRANT SELECT, INSERT, UPDATE, DELETE ON erp_field_mappings TO invoice_app;

CREATE TABLE erp_lookups (
    tenant_id      uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id             uuid        NOT NULL DEFAULT gen_random_uuid(),
    entity_id      uuid        NOT NULL,
    system         text        NOT NULL,
    external_key   text        NOT NULL,  -- the ERP company
    kind           text        NOT NULL,
    erp_key        text        NOT NULL,
    tax_category   text,
    exemption_code text,
    hsn_code       text,
    service_code   text,
    tin            text,
    state          text,
    lga            text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT erp_lookups_pk PRIMARY KEY (id),
    CONSTRAINT erp_lookups_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT erp_lookups_client_company_kind_key_uq UNIQUE (tenant_id, entity_id, system, external_key, kind, erp_key),
    CONSTRAINT erp_lookups_system_check CHECK (system <> ''),
    CONSTRAINT erp_lookups_external_key_check CHECK (char_length(external_key) BETWEEN 1 AND 256 AND external_key = btrim(external_key)),
    CONSTRAINT erp_lookups_kind_check CHECK (kind IN ('tax_code', 'item', 'customer')),
    CONSTRAINT erp_lookups_key_check CHECK (char_length(erp_key) BETWEEN 1 AND 256 AND erp_key = btrim(erp_key)),
    CONSTRAINT erp_lookups_values_check CHECK (
        (kind = 'tax_code' AND tax_category IS NOT NULL
            AND num_nonnulls(hsn_code, service_code, tin, state, lga) = 0)
     OR (kind = 'item' AND num_nonnulls(hsn_code, service_code) = 1
            AND num_nonnulls(tax_category, exemption_code, tin, state, lga) = 0)
     OR (kind = 'customer' AND num_nonnulls(tin, state, lga) >= 1
            AND num_nonnulls(tax_category, exemption_code, hsn_code, service_code) = 0)),
    CONSTRAINT erp_lookups_tin_check CHECK (tin IS NULL OR tin ~ '^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$'),
    CONSTRAINT erp_lookups_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE erp_lookups ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp_lookups FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON erp_lookups USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));
-- DELETE removes an entry whose values were all cleared.
GRANT SELECT, INSERT, UPDATE, DELETE ON erp_lookups TO invoice_app;

-- The effective mapping content_hash was taken under.
ALTER TABLE erp_source_documents ADD COLUMN mapping_fingerprint text
    CONSTRAINT erp_source_documents_mapping_fingerprint_check
        CHECK (mapping_fingerprint IS NULL OR mapping_fingerprint ~ '^[0-9a-f]{64}$');

-- +goose Down
ALTER TABLE erp_source_documents DROP COLUMN mapping_fingerprint;
DROP TABLE erp_lookups;
DROP TABLE erp_field_mappings;
