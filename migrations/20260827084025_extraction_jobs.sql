-- extraction_jobs — one row per document read, updated in place as it progresses.
--
-- The FK is composite (tenant_id, document_id) -> documents (tenant_id, id): Postgres runs
-- referential-integrity checks with RLS BYPASSED, so a bare document_id would silently
-- accept another tenant's document. ON DELETE RESTRICT keeps a document that has been read
-- from vanishing out from under its extraction record.
--
-- No UNIQUE over (tenant_id, document_id) on purpose (D-6): two jobs for one document is
-- tolerated, and a unique index would turn that into a runtime 23505 in the worker.
--
-- updated_at is trigger-maintained rather than trusted to every writer. The function is
-- table-specific — this repo has no shared one — and the Down drops it explicitly.

-- +goose Up
CREATE TABLE extraction_jobs (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id       uuid        NOT NULL,
    state             text        NOT NULL DEFAULT 'queued'
                                  CHECK (state IN ('queued', 'extracting', 'succeeded',
                                                   'failed', 'dead_lettered')),
    attempts          int         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    extractor         text        NOT NULL CHECK (char_length(extractor) > 0),
    extractor_version text        NOT NULL CHECK (char_length(extractor_version) > 0),
    last_error        text,
    river_job_id      bigint,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- A constraint, not a bare unique index: only a pg_constraint row can be a composite-FK
    -- target (extraction_field_results, EXTR-01-02).
    CONSTRAINT extraction_jobs_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT extraction_jobs_tenant_document_fk
        FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, id) ON DELETE RESTRICT
);

-- Per-document job lookup; also services the RESTRICT-delete check from documents.
CREATE INDEX extraction_jobs_tenant_document_idx ON extraction_jobs (tenant_id, document_id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION extraction_jobs_touch_updated_at()
    RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER extraction_jobs_set_updated_at
    BEFORE UPDATE ON extraction_jobs
    FOR EACH ROW EXECUTE FUNCTION extraction_jobs_touch_updated_at();

-- Force is what subjects the table owner (the migrator) to the policy below; enable alone
-- would let it bypass (docs/migrations.md §1).
ALTER TABLE extraction_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_jobs FORCE  ROW LEVEL SECURITY;

-- No TO clause, so the policy binds every role. The USING doubles as the INSERT/UPDATE
-- WITH CHECK; an unset GUC yields NULL and therefore no rows.
CREATE POLICY tenant_isolation ON extraction_jobs
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Least privilege (docs/migrations.md §3). No DELETE: the demo tenants' rows are removed
-- out of band by the boot-time purge running as superuser (docs/demo-reset.md).
GRANT SELECT, INSERT, UPDATE ON extraction_jobs TO invoice_app;

-- +goose Down
-- The table drop takes its policy, indexes, constraints, grants and trigger with it; the
-- function is a separate object.
DROP TABLE extraction_jobs;
DROP FUNCTION extraction_jobs_touch_updated_at;
