-- +goose Up
-- BUG-06-01 (task-383): what kind of dead end a failed submission hit, so the
-- SPA can explain it instead of a bare "failed" badge. NULL is the legacy
-- semantic (AC-7): pre-migration rows, and rows failed through
-- POST /transitions (story BUG-06 S0 F1), carry no kind and are never
-- backfilled. No status-correlating CHECK: markTerminalTx
-- (internal/invoice/actor.go:94-127) writes this column's outcome callback
-- BEFORE transitionTx, so the row still reads its pre-failure status when
-- the CHECK evaluates.
ALTER TABLE invoices
    ADD COLUMN failure_kind text
    CHECK (failure_kind IS NULL OR failure_kind IN
           ('payload_not_built', 'never_acknowledged', 'acknowledged_no_verdict'));

-- No new GRANT: invoices carries a table-level GRANT (20260714103137_invoices.sql:95)
-- covering columns added later. No policy either -- tenant_isolation is row-scoped.

-- +goose Down
ALTER TABLE invoices DROP COLUMN failure_kind;
