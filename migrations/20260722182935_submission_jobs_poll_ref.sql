-- M5-02-05: submission_jobs.poll_ref — the adapter-defined opaque token a deferred verdict
-- (Result.Pending) carries, so a later poll can resume it after a restart ([pending-carries-
-- opaque-ref], Core AC-3: "a deferred verdict survives a restart"). One additive column, no
-- rewrite of anything already here.
--
-- Nullable, because a pending ref genuinely does not exist until an authority defers a
-- verdict — a queued, submitting, accepted, rejected, or failed job never has one. The
-- `IS NULL OR char_length(poll_ref) > 0` CHECK (the same idiom as irn/csid/qr_payload,
-- 20260722083015_invoices_fiscal_outcome.sql:58-60, rationale at :14-17) stops an adapter
-- that writes '' where it meant "nothing" from creating a second encoding of "no ref" —
-- unnamed inline CHECK, dropped with the column, so the Down needs no separate statement.
--
-- No length bound (unlike idempotency_key, which mirrors the M2-08 ledger's own CHECK): there
-- is no second CHECK anywhere in the schema for poll_ref to agree with, and any number picked
-- here would be an invented limit on an authority-defined opaque string whose format this
-- repo does not control.
--
-- No index: this column's own sibling next_poll_at made exactly this ruling
-- (20260722085427_submission_jobs.sql:56-58) — the pending re-poll sweep that would need one
-- does not exist until M5-04/M5-06, and this repo defers indexes to the consumer stories that
-- drive them.
--
-- No new GRANT: submission_jobs already carries a table-level GRANT (same migration, line
-- 125) which covers columns added later, the same argument invoices_fiscal_outcome.sql makes
-- for irn/csid/qr_payload.
--
-- No new POLICY: the table already carries FORCE RLS with the tenant_isolation policy (same
-- migration, lines 110-120), which is row-scoped and so already governs a column added later.
--
-- No writer ships in this story ([no-poll-ref-writer-in-02]) — M5-04 owns the job-state UPDATE
-- that sets it; this migration proves only the schema, via a raw round-trip in a test.
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
ALTER TABLE submission_jobs
    ADD COLUMN poll_ref text CHECK (poll_ref IS NULL OR char_length(poll_ref) > 0);

-- +goose Down
ALTER TABLE submission_jobs DROP COLUMN poll_ref;
