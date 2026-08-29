-- +goose Up
-- Adds extraction.field_corrected to the resolver, keyed on an invoice_id payload key.
-- This is the THIRD migration to define audit_log_entity_for, and the fifth audit_log
-- migration since the table's creation -- four precede it (20260803104130, 20260820150810,
-- 20260821135423, 20260822080722). It ALTERs no table; only three migrations ever have.
-- Q15 waived it. A fourth waiver is the wrong answer: fix the hardcoded-list dispatch instead.
-- A separate ELSIF, not a growth of rule B: rules A and B are set-equal-pinned to the
-- generated invoice_id column, and growing either rewrites that column across the table.
-- Otherwise byte-identical to 20260821135423_audit_log_entity_for_submission_failed.sql.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_entity_for(p_event text, p_payload jsonb)
    RETURNS uuid
    LANGUAGE plpgsql
    STABLE
    SECURITY INVOKER
    AS $fn$
DECLARE
    v_raw    text;
    v_norm   text;
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
                      'submission.accepted', 'submission.rejected', 'submission.failed',
                      'reconciliation.drift_detected', 'reconciliation.auto_fixed') THEN
        v_raw := p_payload->>'invoice_id';
    -- The entity id is already in the payload; no join.
    ELSIF p_event IN ('portfolio.entity.created', 'portfolio.entity.updated',
                      'portfolio.entity.onboarded', 'portfolio.entity.offboarded') THEN
        v_raw    := p_payload->>'id';
        v_direct := true;
    -- Extraction corrections reach a company through the invoice they correct. Kept out
    -- of rule B so the generated invoice_id column's set-equality pins do not move.
    ELSIF p_event IN ('extraction.field_corrected') THEN
        v_raw := p_payload->>'invoice_id';
    -- Workspace-level: nothing to attribute.
    ELSE
        RETURN NULL;
    END IF;

    -- The gate is uuid_in's own grammar, not canonical form: callers echo the raw URL
    -- segment, and a canonical-only check reads a legal id as NULL -- which this column
    -- spells "workspace-level", misfiling a client action as firm-wide. Casting the
    -- hyphen-stripped 32 hex digits is what keeps the cast total, so no admitted spelling
    -- raises 22P02. Fenced by TestAudit_InsertTriggerResolvesEverySpellingUUIDInAccepts.
    IF v_raw IS NULL THEN
        RETURN NULL;
    END IF;
    v_norm := lower(v_raw);
    IF v_norm LIKE '{%}' THEN
        v_norm := substring(v_norm FROM 2 FOR length(v_norm) - 2);
    END IF;
    IF v_norm !~ '^[0-9a-f]{4}(-?[0-9a-f]{4}){7}$' THEN
        RETURN NULL;
    END IF;
    v_norm := replace(v_norm, '-', '');

    IF v_direct THEN
        RETURN v_norm::uuid;
    END IF;
    RETURN (SELECT i.entity_id FROM invoices i WHERE i.id = v_norm::uuid);
END
$fn$;
-- +goose StatementEnd

-- +goose Down
-- Restores AUDIT-03's 21-event body. DROP is wrong here: audit_log_set_entity calls this
-- on every insert, and AUDIT-01's own Down is what drops it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_entity_for(p_event text, p_payload jsonb)
    RETURNS uuid
    LANGUAGE plpgsql
    STABLE
    SECURITY INVOKER
    AS $fn$
DECLARE
    v_raw    text;
    v_norm   text;
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
                      'submission.accepted', 'submission.rejected', 'submission.failed',
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

    -- The gate is uuid_in's own grammar, not canonical form: callers echo the raw URL
    -- segment, and a canonical-only check reads a legal id as NULL -- which this column
    -- spells "workspace-level", misfiling a client action as firm-wide. Casting the
    -- hyphen-stripped 32 hex digits is what keeps the cast total, so no admitted spelling
    -- raises 22P02. Fenced by TestAudit_InsertTriggerResolvesEverySpellingUUIDInAccepts.
    IF v_raw IS NULL THEN
        RETURN NULL;
    END IF;
    v_norm := lower(v_raw);
    IF v_norm LIKE '{%}' THEN
        v_norm := substring(v_norm FROM 2 FOR length(v_norm) - 2);
    END IF;
    IF v_norm !~ '^[0-9a-f]{4}(-?[0-9a-f]{4}){7}$' THEN
        RETURN NULL;
    END IF;
    v_norm := replace(v_norm, '-', '');

    IF v_direct THEN
        RETURN v_norm::uuid;
    END IF;
    RETURN (SELECT i.entity_id FROM invoices i WHERE i.id = v_norm::uuid);
END
$fn$;
-- +goose StatementEnd
