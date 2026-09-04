-- The five kinds the worker already computes, on the row rather than only in the audit
-- payload. Nullable, no default: a success and every pre-migration row carry no kind, the
-- nil-when-clean discipline last_error follows (TestExtractWorker_FailureKindPerStage).
-- Table-level grants and the FOR ALL tenant_isolation policy already cover a new column.

-- +goose Up
ALTER TABLE extraction_jobs ADD COLUMN failure_kind text
    CHECK (failure_kind IS NULL OR failure_kind IN (
        'document_unavailable', 'pages_not_rendered', 'page_rows_not_written',
        'extract_failed', 'text_not_read'));

-- +goose Down
ALTER TABLE extraction_jobs DROP COLUMN failure_kind;
