-- +goose Up
ALTER TABLE memberships ADD COLUMN all_clients boolean NOT NULL DEFAULT false;
ALTER TABLE invitations ADD COLUMN all_clients boolean NOT NULL DEFAULT false;
-- Target of invitation_clients' composite FK.
ALTER TABLE invitations ADD CONSTRAINT invitations_tenant_id_id_uq UNIQUE (tenant_id, id);

CREATE TABLE membership_clients (
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL,
    entity_id  uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT membership_clients_pk PRIMARY KEY (tenant_id, user_id, entity_id),
    -- Composite: RI checks bypass RLS (TestRLS_ClientAccessCrossTenantEntityRefused).
    CONSTRAINT membership_clients_tenant_user_fk FOREIGN KEY (tenant_id, user_id)
        REFERENCES memberships (tenant_id, user_id) ON DELETE CASCADE,
    CONSTRAINT membership_clients_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX membership_clients_entity_idx ON membership_clients (tenant_id, entity_id);

CREATE TABLE invitation_clients (
    tenant_id     uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    invitation_id uuid        NOT NULL,
    entity_id     uuid        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invitation_clients_pk PRIMARY KEY (tenant_id, invitation_id, entity_id),
    CONSTRAINT invitation_clients_tenant_invitation_fk FOREIGN KEY (tenant_id, invitation_id)
        REFERENCES invitations (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT invitation_clients_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX invitation_clients_entity_idx ON invitation_clients (tenant_id, entity_id);

ALTER TABLE membership_clients ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_clients FORCE ROW LEVEL SECURITY;
ALTER TABLE invitation_clients ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitation_clients FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON membership_clients
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
CREATE POLICY tenant_isolation ON invitation_clients
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
-- Whole-set replace is DELETE then INSERT: no UPDATE.
GRANT SELECT, INSERT, DELETE ON membership_clients TO invoice_app;
GRANT SELECT, INSERT, DELETE ON invitation_clients TO invoice_app;

-- +goose Down
DROP TABLE invitation_clients;
DROP TABLE membership_clients;
ALTER TABLE invitations DROP CONSTRAINT invitations_tenant_id_id_uq;
ALTER TABLE invitations DROP COLUMN all_clients;
ALTER TABLE memberships DROP COLUMN all_clients;
