-- +goose Up
-- invoice_id is row-local (event + payload only), unlike entity_id's join through
-- invoices, so it can be a STORED generated column instead of a trigger+backfill:
-- Postgres recomputes it during this rewrite and on every future insert, with no drift
-- possible. Dispatch is on the EVENT NAME, never on which payload key is present --
-- document.created carries a real invoice id under the bare `id` key, exactly like
-- invoice.created's, and a key-scoped resolver would misattribute it.
--
-- Inlined rather than factored into a helper function: a function referenced from a
-- generation expression can be CREATE OR REPLACEd later, after which new rows compute
-- differently while stored rows keep the old value -- a silent, permanent divergence on
-- an append-only table. This expression is frozen in pg_attrdef instead.
--
-- The uuid gate mirrors audit_log_entity_for's grammar and LIKE+substring brace strip
-- byte for byte (docs/audit-log-read-contract.md §6) -- a looser guard raises 22P02 on
-- this ALTER's table-wide rewrite and aborts the whole fleet deploy.

-- +goose StatementBegin
DO $guard$
DECLARE
    v_rows bigint;
BEGIN
    -- invoice_migrator owns audit_log; FORCE RLS binds even the owner and the role is
    -- NOBYPASSRLS, so an unguarded count(*) here would read 0 and never trip.
    ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY;
    SELECT count(*) INTO v_rows FROM audit_log;
    ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
    IF v_rows > 500000 THEN
        RAISE EXCEPTION 'audit_log has % rows, above the 500000 in-migration ceiling; at ~6.5 us/row the ADD COLUMN STORED rewrite has no out-of-band path -- raise the ceiling only after confirming that rewrite time is acceptable, then redeploy', v_rows
            USING ERRCODE = 'program_limit_exceeded';
    END IF;
END
$guard$;
-- +goose StatementEnd

ALTER TABLE audit_log ADD COLUMN invoice_id uuid GENERATED ALWAYS AS (
    CASE
    WHEN event IN ('invoice.created', 'invoice.updated', 'invoice.transitioned',
                   'invoice.validated', 'invoice.kept_as_is', 'invoice.unkept_as_is',
                   'invoice.resolved_outside', 'invoice.unresolved_outside',
                   'invoice.approval_armed', 'invoice.approval_cancelled') THEN
        CASE WHEN
            (CASE WHEN lower(payload->>'id') LIKE '{%}'
                  THEN substring(lower(payload->>'id') FROM 2 FOR length(lower(payload->>'id')) - 2)
                  ELSE lower(payload->>'id')
             END) ~ '^[0-9a-f]{4}(-?[0-9a-f]{4}){7}$'
        THEN replace(
            (CASE WHEN lower(payload->>'id') LIKE '{%}'
                  THEN substring(lower(payload->>'id') FROM 2 FOR length(lower(payload->>'id')) - 2)
                  ELSE lower(payload->>'id')
             END), '-', '')::uuid
        END
    WHEN event IN ('invoice.approval_approved', 'invoice.approval_rejected',
                   'submission.accepted', 'submission.rejected', 'submission.failed',
                   'reconciliation.drift_detected', 'reconciliation.auto_fixed') THEN
        CASE WHEN
            (CASE WHEN lower(payload->>'invoice_id') LIKE '{%}'
                  THEN substring(lower(payload->>'invoice_id') FROM 2 FOR length(lower(payload->>'invoice_id')) - 2)
                  ELSE lower(payload->>'invoice_id')
             END) ~ '^[0-9a-f]{4}(-?[0-9a-f]{4}){7}$'
        THEN replace(
            (CASE WHEN lower(payload->>'invoice_id') LIKE '{%}'
                  THEN substring(lower(payload->>'invoice_id') FROM 2 FOR length(lower(payload->>'invoice_id')) - 2)
                  ELSE lower(payload->>'invoice_id')
             END), '-', '')::uuid
        END
    END
) STORED;

CREATE INDEX audit_log_tenant_invoice_created_idx
    ON audit_log (tenant_id, invoice_id, created_at DESC, id DESC);

-- +goose Down
-- DROP COLUMN auto-drops the index; the explicit DROP INDEX must come first, or it
-- fails because the index is already gone.
DROP INDEX audit_log_tenant_invoice_created_idx;
ALTER TABLE audit_log DROP COLUMN invoice_id;
