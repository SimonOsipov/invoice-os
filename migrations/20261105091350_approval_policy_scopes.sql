-- +goose Up
DROP INDEX approval_policy_versions_one_active;
CREATE UNIQUE INDEX approval_policy_versions_one_active
    ON approval_policy_versions (tenant_id, policy_id) WHERE is_active;

-- DEFAULT is the backfill: every existing version reads as a catch-all, no UPDATE runs.
ALTER TABLE approval_policy_versions
    ADD COLUMN scope_document_types text[] NOT NULL DEFAULT '{}'
        CONSTRAINT approval_policy_versions_scope_document_types_check
        CHECK (scope_document_types <@ ARRAY['commercial_invoice', 'credit_note', 'debit_note']),
    ADD COLUMN scope_entity_ids        uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN scope_workflow_role_ids uuid[] NOT NULL DEFAULT '{}',
    ADD COLUMN scope_amount_lower      numeric(14,2),
    ADD COLUMN scope_amount_upper      numeric(14,2),
    ADD COLUMN scope_amount_currency   text,
    ADD CONSTRAINT approval_policy_versions_scope_amount_currency_check
        CHECK ((scope_amount_currency IS NULL) = (scope_amount_lower IS NULL AND scope_amount_upper IS NULL)),
    ADD CONSTRAINT approval_policy_versions_scope_amount_order_check
        CHECK (scope_amount_lower IS NULL OR scope_amount_upper IS NULL OR scope_amount_lower <= scope_amount_upper);

-- Seal guard plus: a sealed version's scopes never change.
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
    IF OLD.sealed AND (
          OLD.scope_document_types    IS DISTINCT FROM NEW.scope_document_types
       OR OLD.scope_entity_ids        IS DISTINCT FROM NEW.scope_entity_ids
       OR OLD.scope_workflow_role_ids IS DISTINCT FROM NEW.scope_workflow_role_ids
       OR OLD.scope_amount_lower      IS DISTINCT FROM NEW.scope_amount_lower
       OR OLD.scope_amount_upper      IS DISTINCT FROM NEW.scope_amount_upper
       OR OLD.scope_amount_currency   IS DISTINCT FROM NEW.scope_amount_currency) THEN
        RAISE EXCEPTION 'the scopes of a sealed approval policy version cannot change (version=%)', OLD.version
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE approval_policies
    DROP CONSTRAINT approval_policies_scope_check,
    ADD COLUMN position int;

-- FORCE RLS hides every row from the migrator without a tenant GUC; the toggle is transactional.
ALTER TABLE approval_policies NO FORCE ROW LEVEL SECURITY;
UPDATE approval_policies p
   SET position = o.n
  FROM (SELECT id, row_number() OVER (PARTITION BY tenant_id ORDER BY created_at, id) AS n
          FROM approval_policies) o
 WHERE p.id = o.id;
ALTER TABLE approval_policies FORCE ROW LEVEL SECURITY;

ALTER TABLE approval_policies
    ALTER COLUMN position SET NOT NULL,
    -- DEFERRABLE: a reorder swaps positions in one UPDATE.
    ADD CONSTRAINT approval_policies_live_position_excl
        EXCLUDE USING btree (tenant_id WITH =, position WITH =) WHERE (deleted_at IS NULL) DEFERRABLE;

-- +goose StatementBegin
CREATE FUNCTION approval_policies_assign_position() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Serialises concurrent creates in one tenant.
    PERFORM pg_advisory_xact_lock(hashtext('approval_policies.position'), hashtext(NEW.tenant_id::text));
    SELECT coalesce(max(position), 0) + 1 INTO NEW.position
      FROM approval_policies WHERE tenant_id = NEW.tenant_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER approval_policies_assign_position
    BEFORE INSERT ON approval_policies
    FOR EACH ROW WHEN (NEW.position IS NULL)
    EXECUTE FUNCTION approval_policies_assign_position();

-- +goose Down
-- +goose StatementBegin
DO $guard$
DECLARE
    v_tenants bigint;
BEGIN
    ALTER TABLE approval_policy_versions NO FORCE ROW LEVEL SECURITY;
    SELECT count(*) INTO v_tenants FROM (
        SELECT tenant_id FROM approval_policy_versions
         WHERE is_active GROUP BY tenant_id HAVING count(*) > 1) s;
    ALTER TABLE approval_policy_versions FORCE ROW LEVEL SECURITY;
    IF v_tenants > 0 THEN
        RAISE EXCEPTION '% workspace(s) have more than one active policy; deactivate all but one per workspace, then run this Down again', v_tenants
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
END
$guard$;
-- +goose StatementEnd

DROP TRIGGER approval_policies_assign_position ON approval_policies;
DROP FUNCTION approval_policies_assign_position;
ALTER TABLE approval_policies
    DROP CONSTRAINT approval_policies_live_position_excl,
    DROP COLUMN position,
    ADD CONSTRAINT approval_policies_scope_check CHECK (scope = 'All invoices');

-- Seal guard back to its 20260809210326 body, byte for byte.
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

-- Dropping a column fires no row trigger; the CHECKs go with their columns.
ALTER TABLE approval_policy_versions
    DROP COLUMN scope_amount_currency,
    DROP COLUMN scope_amount_upper,
    DROP COLUMN scope_amount_lower,
    DROP COLUMN scope_workflow_role_ids,
    DROP COLUMN scope_entity_ids,
    DROP COLUMN scope_document_types;

DROP INDEX approval_policy_versions_one_active;
CREATE UNIQUE INDEX approval_policy_versions_one_active
    ON approval_policy_versions (tenant_id) WHERE is_active;
