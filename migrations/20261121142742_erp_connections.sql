-- +goose Up
CREATE TABLE erp_grants (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id         uuid        NOT NULL DEFAULT gen_random_uuid(),
    system     text        NOT NULL,
    kind       text        NOT NULL,
    secret_ref text,                       -- Secret Manager name; never a token
    expires_at timestamptz,
    version    bigint      NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT erp_grants_pk PRIMARY KEY (id),
    CONSTRAINT erp_grants_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT erp_grants_kind_check CHECK (kind IN ('oauth_refresh', 'client_credentials', 'api_key'))
);
ALTER TABLE erp_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON erp_grants
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
GRANT SELECT, INSERT, DELETE ON erp_grants TO invoice_app;

CREATE TABLE erp_connections (
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id           uuid        NOT NULL DEFAULT gen_random_uuid(),
    entity_id    uuid        NOT NULL,
    system       text        NOT NULL,
    external_key text,
    status       text        NOT NULL DEFAULT 'pending',
    grant_id     uuid,
    cursor       bytea,
    last_sync_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT erp_connections_pk PRIMARY KEY (id),
    CONSTRAINT erp_connections_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT erp_connections_client_system_key_uq UNIQUE (tenant_id, entity_id, system, external_key),
    CONSTRAINT erp_connections_status_check CHECK (status IN ('pending', 'active', 'reconsent', 'disconnected')),
    CONSTRAINT erp_connections_active_is_complete CHECK (status <> 'active' OR (external_key IS NOT NULL AND grant_id IS NOT NULL)),
    -- Composite: RI checks bypass RLS.
    CONSTRAINT erp_connections_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT erp_connections_tenant_grant_fk FOREIGN KEY (tenant_id, grant_id)
        REFERENCES erp_grants (tenant_id, id)
);
CREATE INDEX erp_connections_grant_idx ON erp_connections (tenant_id, grant_id) WHERE grant_id IS NOT NULL;
ALTER TABLE erp_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp_connections FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON erp_connections USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));
-- Disconnect keeps the row: no DELETE.
GRANT SELECT, INSERT, UPDATE ON erp_connections TO invoice_app;

-- +goose Down
DROP TABLE erp_connections;
DROP TABLE erp_grants;
