-- APPR-08-07: the awaiting_approval List filter's NOT EXISTS anti-join runs per candidate
-- row over approval_runs, and none of the table's three existing indexes serves
-- (tenant_id, invoice_id) for state = 'approved' (approval_runs_one_open is partial on
-- state = 'open'; approval_runs_tenant_id_id_uq's second column is id, not invoice_id).
--
-- tenant_id leads per this repo's convention (D12) so the RLS qual becomes an Index Cond.
-- Not partial on state = 'approved': the same index serves RowFactsTx's DISTINCT ON and
-- GateFactsTx's ORDER BY opened_at DESC LIMIT 1, which read every state. No CONCURRENTLY --
-- goose wraps a migration in a transaction and CONCURRENTLY cannot run inside one.

-- +goose Up
CREATE INDEX approval_runs_invoice_lookup_idx
    ON approval_runs (tenant_id, invoice_id, opened_at DESC);

-- +goose Down
DROP INDEX approval_runs_invoice_lookup_idx;
