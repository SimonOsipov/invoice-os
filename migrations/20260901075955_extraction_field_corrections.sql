-- extraction_field_corrections — one append-only row per human correction to a field. A
-- field's current value is the LATEST row here, never an UPDATE.
--
-- seq (a bigserial, so a sequence) is the order, not created_at: created_at defaults to
-- now(), which is transaction-constant, so two corrections written together tie on it
-- exactly while nextval still separates them.
--
-- The FK is composite (tenant_id, extraction_job_id) -> extraction_jobs (tenant_id, id),
-- the same reasoning as extraction_field_results: RI checks run RLS-bypassed, so a bare
-- extraction_job_id would silently accept another tenant's job.
--
-- Boxes are normalised [0,1], top-left origin, 1-based page — identical to
-- extraction_field_results. A pointed correction MUST carry a region and every other
-- method MUST NOT; _pointed_has_region and _region_complete compose through `page`.
--
-- actor mirrors audit_log.actor: a raw identity string (GoTrue subject or "system"),
-- text plus a length CHECK, no FK.

-- +goose Up
CREATE TABLE extraction_field_corrections (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    extraction_job_id uuid        NOT NULL,
    field_name        text        NOT NULL CHECK (char_length(field_name) > 0
                                              AND char_length(field_name) <= 128),
    value             text        NOT NULL CHECK (char_length(value) > 0),
    method            text        NOT NULL CHECK (method IN
                                     ('typed', 'chosen', 'pointed', 'undone')),
    page              int         CHECK (page IS NULL OR page >= 1),
    bbox_x0           double precision,
    bbox_y0           double precision,
    bbox_x1           double precision,
    bbox_y1           double precision,
    anchor_label      text,
    actor             text        NOT NULL CHECK (char_length(actor) > 0
                                              AND char_length(actor) <= 255),
    seq               bigserial   NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT extraction_field_corrections_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT extraction_field_corrections_tenant_job_fk
        FOREIGN KEY (tenant_id, extraction_job_id)
        REFERENCES extraction_jobs (tenant_id, id) ON DELETE CASCADE,
    -- A region is all five columns or none of them: a half-written box points nowhere.
    CONSTRAINT extraction_field_corrections_region_complete CHECK (
        (page IS NULL     AND bbox_x0 IS NULL     AND bbox_y0 IS NULL
                          AND bbox_x1 IS NULL     AND bbox_y1 IS NULL)
     OR (page IS NOT NULL AND bbox_x0 IS NOT NULL AND bbox_y0 IS NOT NULL
                          AND bbox_x1 IS NOT NULL AND bbox_y1 IS NOT NULL)),
    -- Normalised, top-left origin. An unconverted absolute coordinate fails here rather
    -- than rendering off-screen. A degenerate zero-area box is accepted.
    CONSTRAINT extraction_field_corrections_bbox_normalised CHECK (
        bbox_x0 IS NULL
     OR (bbox_x0 >= 0 AND bbox_x1 <= 1 AND bbox_x0 <= bbox_x1
     AND bbox_y0 >= 0 AND bbox_y1 <= 1 AND bbox_y0 <= bbox_y1)),
    -- Method and region presence agree in both directions. The all-or-none CHECK above
    -- alone admits a typed row carrying a box and a pointed row carrying none.
    CONSTRAINT extraction_field_corrections_pointed_has_region
        CHECK ((method = 'pointed') = (page IS NOT NULL))
);

-- Serves the latest-row read: an index-only backward scan for one field, and the
-- (tenant_id, extraction_job_id, field_name) prefix also serves
-- DISTINCT ON (field_name) ... ORDER BY field_name, seq DESC without a sort.
CREATE INDEX extraction_field_corrections_tenant_job_field_seq_idx
    ON extraction_field_corrections (tenant_id, extraction_job_id, field_name, seq DESC);

ALTER TABLE extraction_field_corrections ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_field_corrections FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON extraction_field_corrections
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Append-only grants (docs/migrations.md §3): SELECT + INSERT, never UPDATE/DELETE. This is
-- what stops a correction being edited in place — invoice_app's UPDATE/DELETE fails 42501
-- before RLS is ever consulted. USAGE on the bigserial sequence is what an INSERT needs to
-- call nextval; the table grant does not carry it.
GRANT SELECT, INSERT ON extraction_field_corrections TO invoice_app;
GRANT USAGE ON SEQUENCE extraction_field_corrections_seq_seq TO invoice_app;

-- +goose Down
DROP TABLE extraction_field_corrections;
