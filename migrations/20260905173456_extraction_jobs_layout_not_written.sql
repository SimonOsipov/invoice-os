-- The sixth kind: the reader read the document but its boxless layout could not be stored.
-- Lands before the worker stage that assigns it, so no commit writes a value the CHECK refuses.
-- Postgres auto-named the inline CHECK from EXTR-15-01 extraction_jobs_failure_kind_check.

-- +goose Up
ALTER TABLE extraction_jobs DROP CONSTRAINT extraction_jobs_failure_kind_check;
ALTER TABLE extraction_jobs ADD CONSTRAINT extraction_jobs_failure_kind_check
    CHECK (failure_kind IS NULL OR failure_kind IN (
        'document_unavailable', 'pages_not_rendered', 'page_rows_not_written',
        'extract_failed', 'text_not_read', 'layout_not_written'));

-- +goose Down
-- Lossy on purpose: the five-value CHECK cannot be restored while a row holds the sixth, and
-- failure_kind is a nullable diagnostic column, so the row reverts to dead_lettered with no
-- kind -- a shipped rendering. state and last_error survive.
--
-- The FORCE toggle is load-bearing. extraction_jobs is FORCE ROW LEVEL SECURITY and owned by
-- invoice_migrator, and goose runs this with no app.current_tenant: without it the UPDATE sees
-- zero rows while ADD CONSTRAINT validates all of them, and the Down aborts with 23514.
-- TestExtractionFailureKind_LayoutNotWrittenMigrationRoundTrips runs the Down tenant-less.
ALTER TABLE extraction_jobs NO FORCE ROW LEVEL SECURITY;
UPDATE extraction_jobs SET failure_kind = NULL WHERE failure_kind = 'layout_not_written';
ALTER TABLE extraction_jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE extraction_jobs DROP CONSTRAINT extraction_jobs_failure_kind_check;
ALTER TABLE extraction_jobs ADD CONSTRAINT extraction_jobs_failure_kind_check
    CHECK (failure_kind IS NULL OR failure_kind IN (
        'document_unavailable', 'pages_not_rendered', 'page_rows_not_written',
        'extract_failed', 'text_not_read'));
