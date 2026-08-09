-- +goose Up
CREATE TABLE workflow_roles (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    key         text        NOT NULL,
    title       text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    deleted_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- Spans soft-deleted rows on purpose: a re-minted key must never inherit a sealed
    -- policy step (TestRLS_WorkflowRolesKeyUniquePerTenantSpansDeleted).
    CONSTRAINT workflow_roles_tenant_key_uq UNIQUE (tenant_id, key),
    -- Sole purpose: the target of the composite FK below. A composite FK can reference a
    -- CONSTRAINT only, never a bare unique index
    -- (TestRLS_WorkflowRolesTenantIdIdUniqueConstraintExists).
    CONSTRAINT workflow_roles_tenant_id_id_uq UNIQUE (tenant_id, id)
);

CREATE TABLE workflow_role_members (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    workflow_role_id uuid        NOT NULL,
    user_id          uuid        NOT NULL,
    -- No DEFAULT: the whole-set replace always supplies the 0-based index, and a silent
    -- DEFAULT 0 would collapse a submitted order undetected
    -- (TestRLS_WorkflowRoleMembersOrdNotNull).
    ord              int         NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT workflow_role_members_tenant_role_user_uq
        UNIQUE (tenant_id, workflow_role_id, user_id),
    -- Composite, not single-column: referential-integrity checks run with RLS bypassed, so
    -- REFERENCES workflow_roles(id) would accept another tenant's row
    -- (TestRLS_WorkflowRoleMembersCrossTenantUserRefused covers the memberships leg).
    CONSTRAINT workflow_role_members_tenant_role_fk
        FOREIGN KEY (tenant_id, workflow_role_id)
        REFERENCES workflow_roles (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT workflow_role_members_tenant_user_fk
        FOREIGN KEY (tenant_id, user_id)
        REFERENCES memberships (tenant_id, user_id) ON DELETE CASCADE
);

-- Force is what subjects the table owner to the policy; enable alone would not
-- (TestRLS_WorkflowRolesOwnerInsertRefusedUnderForce).
ALTER TABLE workflow_roles        ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_roles        FORCE  ROW LEVEL SECURITY;
ALTER TABLE workflow_role_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_role_members FORCE  ROW LEVEL SECURITY;

-- No TO clause, so it binds every role, and the USING doubles as the INSERT/UPDATE
-- WITH CHECK. No tenant_enumerate policy: nothing reads workflow roles cross-tenant
-- (TestRLS_WorkflowRolesForceRLSAndPoliciesDeclared).
CREATE POLICY tenant_isolation ON workflow_roles
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
CREATE POLICY tenant_isolation ON workflow_role_members
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- No DELETE: deleting a role is an UPDATE of deleted_at. UPDATE is also what makes the
-- staffing path's SELECT ... FOR UPDATE on the role row legal
-- (TestRLS_WorkflowRolesSelectForUpdateAllowedForApp).
GRANT SELECT, INSERT, UPDATE ON workflow_roles        TO invoice_app;
-- No UPDATE: staffing arrives as a whole-set replace, i.e. DELETE then INSERT.
GRANT SELECT, INSERT, DELETE ON workflow_role_members TO invoice_app;

-- +goose Down
-- Members first: it holds the FK to workflow_roles. Policies and grants die with the tables.
DROP TABLE workflow_role_members;
DROP TABLE workflow_roles;
