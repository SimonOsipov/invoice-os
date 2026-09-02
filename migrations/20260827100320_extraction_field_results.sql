-- extraction_field_results — one row per candidate reading per field per job (candidate_rank,
-- added later, discriminates the decided reading from its alternatives). Append-only by grant.
--
-- The FK is composite (tenant_id, extraction_job_id) -> extraction_jobs (tenant_id, id):
-- referential-integrity checks run with RLS bypassed, so a bare extraction_job_id would
-- silently accept another tenant's job. ON DELETE CASCADE here, unlike extraction_jobs'
-- RESTRICT to documents — a field result has no meaning without its job, the same shape as
-- approval_run_steps -> approval_runs. The purge still deletes leaf-first so its per-table
-- counts stay honest.
--
-- Boxes are normalised [0,1], top-left origin, plus a 1-based page (D-3). An unconverted
-- absolute coordinate fails _bbox_normalised rather than rendering off-screen two stories
-- later. A degenerate zero-area box is accepted: useless to draw, not wrong.
--
-- No updated_at and no trigger: SELECT and INSERT are the only grants.

-- +goose Up
CREATE TABLE extraction_field_results (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    extraction_job_id uuid        NOT NULL,
    field_name        text        NOT NULL CHECK (char_length(field_name) > 0
                                              AND char_length(field_name) <= 128),
    value             text        CHECK (value IS NULL OR char_length(value) > 0),
    page              int         CHECK (page IS NULL OR page >= 1),
    bbox_x0           double precision,
    bbox_y0           double precision,
    bbox_x1           double precision,
    bbox_y1           double precision,
    reason_code       text        CHECK (reason_code IS NULL OR reason_code IN
                                     ('unreadable', 'ambiguous', 'inconsistent', 'missing')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT extraction_field_results_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT extraction_field_results_tenant_job_fk
        FOREIGN KEY (tenant_id, extraction_job_id)
        REFERENCES extraction_jobs (tenant_id, id) ON DELETE CASCADE,
    -- A region is all five columns or none of them: a half-written box points nowhere.
    CONSTRAINT extraction_field_results_region_complete CHECK (
        (page IS NULL     AND bbox_x0 IS NULL     AND bbox_y0 IS NULL
                          AND bbox_x1 IS NULL     AND bbox_y1 IS NULL)
     OR (page IS NOT NULL AND bbox_x0 IS NOT NULL AND bbox_y0 IS NOT NULL
                          AND bbox_x1 IS NOT NULL AND bbox_y1 IS NOT NULL)),
    -- Normalised, top-left origin. An unconverted absolute coordinate (612.0, 792.0) fails
    -- here rather than rendering off-screen two stories later.
    CONSTRAINT extraction_field_results_bbox_normalised CHECK (
        bbox_x0 IS NULL
     OR (bbox_x0 >= 0 AND bbox_x1 <= 1 AND bbox_x0 <= bbox_x1
     AND bbox_y0 >= 0 AND bbox_y1 <= 1 AND bbox_y0 <= bbox_y1))
);

CREATE INDEX extraction_field_results_tenant_job_idx
    ON extraction_field_results (tenant_id, extraction_job_id);

ALTER TABLE extraction_field_results ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_field_results FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON extraction_field_results
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Append-only by grant. A per-field correction is a NEW row in extraction_field_corrections;
-- these rows are what a correction supersedes and must never be edited in place.
GRANT SELECT, INSERT ON extraction_field_results TO invoice_app;

-- +goose Down
DROP TABLE extraction_field_results;
