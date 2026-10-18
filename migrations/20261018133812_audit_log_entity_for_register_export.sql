-- +goose Up
-- Adds the register-export arm: the payload carries the client's entity_id.
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
    ELSIF p_event IN ('extraction.field_corrected', 'extraction.anchor.learned') THEN
        v_raw := p_payload->>'invoice_id';
    -- Custom-rule saves: the entity id is in the payload; NULL means firm-wide.
    ELSIF p_event IN ('custom_rule.created', 'custom_rule.updated', 'custom_rule.deleted') THEN
        v_raw    := p_payload->>'entity_id';
        v_direct := true;
    -- Register export: the entity id is in the payload; no join.
    ELSIF p_event IN ('invoice_register.exported') THEN
        v_raw    := p_payload->>'entity_id';
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

-- +goose Down
-- Restores 20261011215535's Up body; DROP would break audit_log_set_entity.
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
    ELSIF p_event IN ('extraction.field_corrected', 'extraction.anchor.learned') THEN
        v_raw := p_payload->>'invoice_id';
    -- Custom-rule saves: the entity id is in the payload; NULL means firm-wide.
    ELSIF p_event IN ('custom_rule.created', 'custom_rule.updated', 'custom_rule.deleted') THEN
        v_raw    := p_payload->>'entity_id';
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
