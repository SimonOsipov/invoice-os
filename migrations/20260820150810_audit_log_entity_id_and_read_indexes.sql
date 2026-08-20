-- +goose Up
-- No FK on entity_id: this table's tenant_id has none for the same reason, and under
-- either referential action a delete of the referenced row must UPDATE or DELETE an
-- append-only table, which its own trigger refuses.
--
-- tenant_id leads every index so the RLS qual is a leakproof uuid_eq the planner can
-- turn into an Index Cond; the trailing id DESC makes the keyset page a pure index read.
-- They are built below the backfill so its UPDATEs never maintain a half-built index.
--
-- The whole file must stay inside goose's transaction, which also rules out CONCURRENTLY.
-- Its backfill half suspends the append-only trigger, and only crash-rollback of a single
-- transaction guarantees a process death never leaves that trigger disabled.
--
-- lock_timeout bounds only how long this migration WAITS for a lock. There is deliberately
-- no per-statement bound: aborting a legitimately slow backfill mid-deploy would leave the
-- fleet down for no gain, and the size guard below is the real ceiling.
SET LOCAL lock_timeout = '15s';

ALTER TABLE audit_log ADD COLUMN entity_id uuid;

-- The attribution rules live here and nowhere else -- the backfill below and the
-- write-time trigger at the foot of this file both call this one function.
--
-- Dispatch is on the event NAME, never on which payload key is present: three
-- workspace-level events carry a bare `id` that is a documents id, and six carry a
-- non-uuid text key. SECURITY INVOKER (spelled out because it is load-bearing, not
-- decorative) runs the invoices lookup under the CALLER's RLS, which is what keeps both
-- the backfill and the trigger inside one tenant.
-- +goose StatementBegin
CREATE FUNCTION audit_log_entity_for(p_event text, p_payload jsonb)
    RETURNS uuid
    LANGUAGE plpgsql
    STABLE
    SECURITY INVOKER
    AS $fn$
DECLARE
    v_raw    text;
    v_direct boolean := false;
BEGIN
    -- Invoice-scoped, bare `id`.
    IF p_event IN ('invoice.created', 'invoice.updated', 'invoice.transitioned',
                   'invoice.validated', 'invoice.kept_as_is', 'invoice.unkept_as_is',
                   'invoice.resolved_outside', 'invoice.unresolved_outside',
                   'invoice.approval_armed', 'invoice.approval_cancelled') THEN
        v_raw := p_payload->>'id';
    -- Invoice-scoped, `invoice_id` spelling.
    ELSIF p_event IN ('invoice.approval_approved', 'invoice.approval_rejected',
                      'submission.accepted', 'submission.rejected',
                      'reconciliation.drift_detected', 'reconciliation.auto_fixed') THEN
        v_raw := p_payload->>'invoice_id';
    -- The entity id is already in the payload; no join.
    ELSIF p_event IN ('portfolio.entity.created', 'portfolio.entity.updated',
                      'portfolio.entity.onboarded', 'portfolio.entity.offboarded') THEN
        v_raw    := p_payload->>'id';
        v_direct := true;
    -- Workspace-level: nothing to attribute.
    ELSE
        RETURN NULL;
    END IF;

    -- Shape-checked before the cast, so a malformed id yields NULL instead of aborting
    -- the caller's transaction with 22P02.
    IF v_raw IS NULL OR v_raw !~ '^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$' THEN
        RETURN NULL;
    END IF;

    IF v_direct THEN
        RETURN v_raw::uuid;
    END IF;
    RETURN (SELECT i.entity_id FROM invoices i WHERE i.id = v_raw::uuid);
END
$fn$;
-- +goose StatementEnd

-- The backfill bracket. Lifting FORCE RLS is what lets the guard and the tenant list see
-- every tenant at once; FORCE is back on for the UPDATEs, so RLS itself -- not a WHERE
-- clause -- is what stops a row being attributed across tenants. The WHERE is a cost
-- filter only: a payload carrying neither key can never resolve.
-- +goose StatementBegin
DO $backfill$
DECLARE
    v_tenant uuid;
    v_rows   bigint;
BEGIN
    ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update_delete;

    ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY;
    CREATE TEMP TABLE audit_backfill_tenants ON COMMIT DROP AS
        SELECT DISTINCT tenant_id FROM audit_log;
    SELECT count(*) INTO v_rows FROM audit_log;
    -- Tripping this fails the whole fleet deploy, so the message is the operator's only
    -- instruction.
    IF v_rows > 500000 THEN
        RAISE EXCEPTION 'audit_log has % rows, above the 500000 in-migration ceiling; backfill entity_id out of band, then redeploy', v_rows
            USING ERRCODE = 'program_limit_exceeded';
    END IF;
    ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;

    FOR v_tenant IN SELECT tenant_id FROM audit_backfill_tenants LOOP
        PERFORM set_config('app.current_tenant', v_tenant::text, true);
        UPDATE audit_log SET entity_id = audit_log_entity_for(event, payload)
         WHERE jsonb_exists(payload, 'id') OR jsonb_exists(payload, 'invoice_id');
    END LOOP;
    PERFORM set_config('app.current_tenant', '', true);

    ALTER TABLE audit_log ENABLE TRIGGER audit_log_no_update_delete;
END
$backfill$;
-- +goose StatementEnd

CREATE INDEX audit_log_tenant_created_idx
    ON audit_log (tenant_id, created_at DESC, id DESC);

CREATE INDEX audit_log_tenant_event_created_idx
    ON audit_log (tenant_id, event, created_at DESC, id DESC);

CREATE INDEX audit_log_tenant_actor_created_idx
    ON audit_log (tenant_id, actor, created_at DESC, id DESC);

CREATE INDEX audit_log_tenant_entity_created_idx
    ON audit_log (tenant_id, entity_id, created_at DESC, id DESC);

-- Write-time attribution, created last so the suspend/restore bracket above spans no
-- unrelated trigger DDL. BEFORE INSERT only: it never fires on the backfill's UPDATEs.
-- +goose StatementBegin
CREATE FUNCTION audit_log_set_entity()
    RETURNS trigger
    LANGUAGE plpgsql
    SECURITY INVOKER
    AS $fn$
BEGIN
    NEW.entity_id := audit_log_entity_for(NEW.event, NEW.payload);
    RETURN NEW;
END
$fn$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_entity_on_insert
    BEFORE INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_set_entity();

-- +goose Down
-- DROP COLUMN removes neither the trigger nor either function, and auto-drops only the
-- entity index; the other three must be named. Keep the entity DROP INDEX ahead of the
-- column drop -- after it, the index is already gone.
DROP TRIGGER audit_log_entity_on_insert ON audit_log;
DROP FUNCTION audit_log_set_entity;
DROP FUNCTION audit_log_entity_for;
DROP INDEX audit_log_tenant_entity_created_idx;
DROP INDEX audit_log_tenant_actor_created_idx;
DROP INDEX audit_log_tenant_event_created_idx;
DROP INDEX audit_log_tenant_created_idx;
ALTER TABLE audit_log DROP COLUMN entity_id;
