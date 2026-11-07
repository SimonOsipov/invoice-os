-- +goose Up
ALTER TABLE approval_policy_steps
    ADD COLUMN allow_delegation boolean NOT NULL DEFAULT false,
    ADD COLUMN delegate_user_id uuid,
    ADD CONSTRAINT approval_policy_steps_delegation_kind
        CHECK (NOT allow_delegation OR kind = 'approval'),
    ADD CONSTRAINT approval_policy_steps_delegate_needs_allow
        CHECK (delegate_user_id IS NULL OR allow_delegation),
    ADD CONSTRAINT approval_policy_steps_tenant_delegate_fk
        FOREIGN KEY (tenant_id, delegate_user_id) REFERENCES memberships (tenant_id, user_id);

ALTER TABLE approval_run_steps
    ADD COLUMN allow_delegation boolean NOT NULL DEFAULT false,
    ADD COLUMN delegate_user_id uuid,
    ADD CONSTRAINT approval_run_steps_delegation_kind
        CHECK (NOT allow_delegation OR kind = 'approval'),
    ADD CONSTRAINT approval_run_steps_delegate_needs_allow
        CHECK (delegate_user_id IS NULL OR allow_delegation),
    ADD CONSTRAINT approval_run_steps_tenant_delegate_fk
        FOREIGN KEY (tenant_id, delegate_user_id) REFERENCES memberships (tenant_id, user_id);

ALTER TABLE approval_decisions
    ADD COLUMN as_delegate boolean NOT NULL DEFAULT false;

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
      OR OLD.allow_delegation  IS DISTINCT FROM NEW.allow_delegation
      OR OLD.delegate_user_id  IS DISTINCT FROM NEW.delegate_user_id
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

-- +goose Down
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

ALTER TABLE approval_decisions DROP COLUMN as_delegate;

ALTER TABLE approval_run_steps
    DROP CONSTRAINT approval_run_steps_tenant_delegate_fk,
    DROP CONSTRAINT approval_run_steps_delegate_needs_allow,
    DROP CONSTRAINT approval_run_steps_delegation_kind,
    DROP COLUMN delegate_user_id,
    DROP COLUMN allow_delegation;

ALTER TABLE approval_policy_steps
    DROP CONSTRAINT approval_policy_steps_tenant_delegate_fk,
    DROP CONSTRAINT approval_policy_steps_delegate_needs_allow,
    DROP CONSTRAINT approval_policy_steps_delegation_kind,
    DROP COLUMN delegate_user_id,
    DROP COLUMN allow_delegation;
