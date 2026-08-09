-- The three approval policy-config tables: approval_policies, approval_policy_versions,
-- approval_policy_steps. The seal lock (active ⟹ sealed CHECK + the one-active-per-tenant
-- partial unique index) is deliberately NOT here — APPR-03-04 extends this same file with
-- it. The `sealed`/`is_active` columns do ship: they exist independent of the lock.
--
-- workflow_role_key on approval_policy_steps is a deliberate non-FK (plain nullable text,
-- no CHECK). Compensating controls, both by name: (i) workflow_roles_tenant_key_uq spans
-- soft-deleted rows, so a key is never re-minted onto a different role; (ii) the key is
-- never re-derived on rename. Weaken either and a published policy silently changes who
-- signs it.
--
-- scope CHECK (scope = 'All invoices') is a storage-layer lock on a Q7 product decision,
-- not a schema nicety — when per-scope routing lands it is a migration, not a config
-- change.
--
-- approval_policy_steps_depth_cap forbids a condition CHILD, not depth — approval <-
-- approval <- ... chains are schema-legal; the shipped SPA type system cannot represent
-- them; rejecting them is a future write-time validation obligation, not this CHECK's job.
-- Do not read this as "depth is capped at two".
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE approval_policies (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name       text        NOT NULL,
    scope      text        NOT NULL DEFAULT 'All invoices' CHECK (scope = 'All invoices'),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approval_policies_tenant_id_id_uq UNIQUE (tenant_id, id)
);

CREATE TABLE approval_policy_versions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    policy_id    uuid        NOT NULL,
    version      int         NOT NULL,
    sealed       boolean     NOT NULL DEFAULT false,
    is_active    boolean     NOT NULL DEFAULT false,
    published_at timestamptz,
    published_by text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approval_policy_versions_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT approval_policy_versions_tenant_policy_version_uq UNIQUE (tenant_id, policy_id, version),
    CONSTRAINT approval_policy_versions_tenant_policy_fk
        FOREIGN KEY (tenant_id, policy_id)
        REFERENCES approval_policies (tenant_id, id) ON DELETE CASCADE
    -- No approval_policy_versions_active_is_sealed CHECK here — APPR-03-04 scope.
);
-- No approval_policy_versions_one_active index here — APPR-03-04 scope.

CREATE TABLE approval_policy_steps (
    id                uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid          NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    version_id        uuid          NOT NULL,
    parent_step_id    uuid,
    branch            text          CHECK (branch IN ('then','else')),
    ord               int           NOT NULL,
    kind              text          NOT NULL
                                    CHECK (kind IN ('approval','condition','notify','autoapprove')),
    workflow_role_key text,
    sla_hours         int,
    cond_op           text          CHECK (cond_op IN ('>','>=','<','<=')),
    cond_amount       numeric(14,2),
    notify_target     text,
    notify_channel    text,
    created_at        timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT approval_policy_steps_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT approval_policy_steps_slot_uq
        UNIQUE NULLS NOT DISTINCT (version_id, parent_step_id, branch, ord),
    CONSTRAINT approval_policy_steps_tenant_version_fk
        FOREIGN KEY (tenant_id, version_id)
        REFERENCES approval_policy_versions (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT approval_policy_steps_tenant_parent_fk
        FOREIGN KEY (tenant_id, parent_step_id)
        REFERENCES approval_policy_steps (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT approval_policy_steps_depth_cap
        CHECK (parent_step_id IS NULL OR kind <> 'condition')
);

ALTER TABLE approval_policies        ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_policies        FORCE  ROW LEVEL SECURITY;
ALTER TABLE approval_policy_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_policy_versions FORCE  ROW LEVEL SECURITY;
ALTER TABLE approval_policy_steps    ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_policy_steps    FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON approval_policies
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
CREATE POLICY tenant_isolation ON approval_policy_versions
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);
CREATE POLICY tenant_isolation ON approval_policy_steps
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE         ON approval_policies        TO invoice_app;
GRANT SELECT, INSERT, UPDATE         ON approval_policy_versions TO invoice_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON approval_policy_steps    TO invoice_app;
-- invoice_tenant_reader gets nothing on any of the three (§4) — no grant lines for it.
-- No TRUNCATE, no REFERENCES, no sequence grants anywhere (every PK is a defaulted uuid).

-- +goose Down
DROP TABLE approval_policy_steps;
DROP TABLE approval_policy_versions;
DROP TABLE approval_policies;
