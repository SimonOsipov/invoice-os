-- +goose Up
-- The FOURTH definition of audit_log_entity_for. It adds extraction.anchor.learned to the
-- extraction arm so a learned layout rule is attributed to the company whose invoice taught
-- it, rather than reading as firm-wide. A separate ELSIF, never a growth of rule A or B:
-- those two are set-equal-pinned to the generated invoice_id column
-- (TestAudit_GeneratedInvoiceIDListsMatchTheLiveResolver).
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
-- Restores 20260829195203's Up body byte for byte. DROP is wrong: audit_log_set_entity calls
-- this on every insert (TestExtraction_AnchorLearnedMigrationDownRestoresTheExtractionBody).
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
