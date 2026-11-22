-- +goose Up
CREATE TABLE erp_oauth_states (
    tenant_id     uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id            uuid        NOT NULL DEFAULT gen_random_uuid(),
    state_hash    bytea       NOT NULL,
    connection_id uuid        NOT NULL,
    user_id       uuid        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    used_at       timestamptz,
    CONSTRAINT erp_oauth_states_pk PRIMARY KEY (id),
    CONSTRAINT erp_oauth_states_hash_uq UNIQUE (state_hash),
    CONSTRAINT erp_oauth_states_hash_len CHECK (octet_length(state_hash) = 32),
    -- Composite: RI checks bypass RLS.
    CONSTRAINT erp_oauth_states_tenant_connection_fk FOREIGN KEY (tenant_id, connection_id)
        REFERENCES erp_connections (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE erp_oauth_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE erp_oauth_states FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON erp_oauth_states
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
GRANT SELECT, INSERT, DELETE ON erp_oauth_states TO invoice_app;
GRANT UPDATE (used_at) ON erp_oauth_states TO invoice_app;

-- The refresh compare-and-swap; secret_ref is written at insert only.
GRANT UPDATE (version, expires_at, updated_at) ON erp_grants TO invoice_app;

-- One keyless pending row per client and system: a repeated connect reuses it.
CREATE UNIQUE INDEX erp_connections_one_keyless_pending
    ON erp_connections (tenant_id, entity_id, system) WHERE external_key IS NULL AND status = 'pending';

-- +goose Down
DROP INDEX erp_connections_one_keyless_pending;
REVOKE UPDATE (version, expires_at, updated_at) ON erp_grants FROM invoice_app;
DROP TABLE erp_oauth_states;
