-- M5-06-01: the `next_poll_at` index submission_jobs' own header names M5-06 as owner
-- (20260722085427_submission_jobs.sql:56-58) — the reconciliation sweep's L1 "lost poll"
-- and H2 "pending too long" signatures both range-scan pending jobs by next_poll_at /
-- created_at, and until now nothing indexed that column.
--
-- Partial + tenant-leading, not a full index ([next-poll-index-is-partial]): L1/H2 only
-- ever scan `state = 'pending'` rows, so a full index would carry every terminal job for
-- no benefit; tenant_id leads to match this repo's tenant-first index convention (D12,
-- 20260714103137_invoices.sql:79-80) since every scan runs per-tenant inside
-- db.WithinTenantTx. No CONCURRENTLY: this repo's existing CREATE INDEX migrations
-- (invoices_entity_status_idx, invoices_import_batch_id_idx) run inside goose's default
-- transaction, and submission_jobs is a young, low-volume table at pilot scale.

-- +goose Up
CREATE INDEX submission_jobs_tenant_next_poll_idx
    ON submission_jobs (tenant_id, next_poll_at)
    WHERE state = 'pending';

-- +goose Down
DROP INDEX submission_jobs_tenant_next_poll_idx;
