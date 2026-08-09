-- The three approval policy-config tables (approval_policies, approval_policy_versions,
-- approval_policy_steps) and the five-mechanism seal lock that makes a published version
-- immutable.
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
-- Every $$-quoted function body below is fenced with goose's StatementBegin/StatementEnd
-- markers: goose splits statements on semicolons and the bodies contain them (precedent:
-- 20260722085427_submission_jobs.sql:94-104).
--
-- Lock provenance: transcribed from 20260717120000_rule_immutability_lock.sql (M4-17 — the
-- child content lock, the TRUNCATE lock, the parent seal guard) and
-- 20260717193449_active_seal_invariant.sql (M4-18 — active => sealed, which closed the
-- active-but-unsealed hole M4-17 left open). Both ship together so that hole never exists.
--
-- approval_policy_versions_seal_guard reads OLD.sealed DIRECTLY off the departing row, never
-- via subquery. On DELETE FROM approval_policy_versions the ON DELETE CASCADE removes the
-- parent before the cascaded step DELETEs run, so the content lock's subquery would find zero
-- rows -> NULL -> never raise, silently destroying a sealed version's steps. Reading OLD.sealed
-- here aborts the statement before the cascade proceeds (M4-17's F1 fix, ...120000:106-114).
--
-- No carve-out, unlike rules_content_lock: `rules` exempts `enabled` (the M3-06 kill-switch);
-- a step has no live-mutable column, so all 14 columns including id and tenant_id are compared.
-- A genuine no-op UPDATE still passes.
--
-- The content lock's subquery reads a FORCE-RLS table, so it is policy-filtered. Safe without
-- SECURITY DEFINER: the composite FK guarantees step and version share a tenant_id and both
-- carry the same tenant_isolation policy, so any session that can mutate the step can see its
-- version. Measured: invoice_app INSERT under a sealed version raises 23001, not 42501.
--
-- One TRUNCATE trigger on the child covers every path: TRUNCATE approval_policy_versions or
-- approval_policies alone is already refused by Postgres (a table outside the statement
-- references them), so the only statement that can legally reach these tables names
-- approval_policy_steps.
--
-- Consequence: a sealed version makes its tenant undeletable — tenants -> policies -> versions
-- is CASCADE the whole way and the guard raises 23001 on the cascaded version DELETE. Teardown
-- needs SET LOCAL session_replication_role = 'replica' plus explicit bottom-up child deletes
-- (teardownSealedApprovalFixture, internal/approval/policy_immutability_test.go).

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
        REFERENCES approval_policies (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT approval_policy_versions_active_is_sealed CHECK (NOT is_active OR sealed)
);

CREATE UNIQUE INDEX approval_policy_versions_one_active
    ON approval_policy_versions (tenant_id) WHERE is_active;

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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION approval_policy_steps_content_lock()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF (SELECT sealed FROM approval_policy_versions WHERE id = OLD.version_id) THEN
            RAISE EXCEPTION 'steps of a sealed approval policy version are immutable: % is not permitted', TG_OP
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF (SELECT sealed FROM approval_policy_versions WHERE id = NEW.version_id) THEN
            RAISE EXCEPTION 'steps of a sealed approval policy version are immutable: % is not permitted', TG_OP
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN NEW;
    END IF;

    IF ( OLD.id                IS DISTINCT FROM NEW.id
      OR OLD.tenant_id         IS DISTINCT FROM NEW.tenant_id
      OR OLD.version_id        IS DISTINCT FROM NEW.version_id
      OR OLD.parent_step_id    IS DISTINCT FROM NEW.parent_step_id
      OR OLD.branch            IS DISTINCT FROM NEW.branch
      OR OLD.ord               IS DISTINCT FROM NEW.ord
      OR OLD.kind              IS DISTINCT FROM NEW.kind
      OR OLD.workflow_role_key IS DISTINCT FROM NEW.workflow_role_key
      OR OLD.sla_hours         IS DISTINCT FROM NEW.sla_hours
      OR OLD.cond_op           IS DISTINCT FROM NEW.cond_op
      OR OLD.cond_amount       IS DISTINCT FROM NEW.cond_amount
      OR OLD.notify_target     IS DISTINCT FROM NEW.notify_target
      OR OLD.notify_channel    IS DISTINCT FROM NEW.notify_channel
      OR OLD.created_at        IS DISTINCT FROM NEW.created_at ) THEN
        IF (SELECT sealed FROM approval_policy_versions WHERE id = OLD.version_id)
           OR (SELECT sealed FROM approval_policy_versions WHERE id = NEW.version_id) THEN
            RAISE EXCEPTION 'steps of a sealed approval policy version are immutable: content UPDATE is not permitted'
                USING ERRCODE = 'restrict_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER approval_policy_steps_content_lock
    BEFORE INSERT OR UPDATE OR DELETE ON approval_policy_steps
    FOR EACH ROW EXECUTE FUNCTION approval_policy_steps_content_lock();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION approval_policy_steps_no_truncate()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'approval_policy_steps is protected by the policy immutability lock: % is not permitted', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER approval_policy_steps_no_truncate
    BEFORE TRUNCATE ON approval_policy_steps
    FOR EACH STATEMENT EXECUTE FUNCTION approval_policy_steps_no_truncate();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION approval_policy_versions_seal_guard()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.sealed THEN
            RAISE EXCEPTION 'a sealed approval policy version cannot be deleted (version=%)', OLD.version
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    IF OLD.sealed AND NOT NEW.sealed THEN
        RAISE EXCEPTION 'a sealed approval policy version cannot be unsealed (version=%)', OLD.version
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER approval_policy_versions_seal_guard
    BEFORE UPDATE OR DELETE ON approval_policy_versions
    FOR EACH ROW EXECUTE FUNCTION approval_policy_versions_seal_guard();

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
DROP TRIGGER approval_policy_versions_seal_guard ON approval_policy_versions;
DROP TRIGGER approval_policy_steps_no_truncate   ON approval_policy_steps;
DROP TRIGGER approval_policy_steps_content_lock  ON approval_policy_steps;

DROP TABLE approval_policy_steps;
DROP TABLE approval_policy_versions;
DROP TABLE approval_policies;

DROP FUNCTION approval_policy_versions_seal_guard;
DROP FUNCTION approval_policy_steps_no_truncate;
DROP FUNCTION approval_policy_steps_content_lock;
