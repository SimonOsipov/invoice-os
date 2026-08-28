-- extraction_page_images — one row per rendered page of a document. The inventory of what a
-- review canvas can draw; the pixels themselves live in object storage under storage_key.
--
-- Keyed on the DOCUMENT, not the extraction job. The object key derives from the document's
-- content hash, so two jobs over one document render byte-identical pixels to the same keys;
-- a job-keyed row set would duplicate rows pointing at one set of objects, and
-- extraction_jobs deliberately carries no UNIQUE over (tenant_id, document_id).
--
-- The FK is composite (tenant_id, document_id) -> documents (tenant_id, id): referential-
-- integrity checks run with RLS BYPASSED, so a bare document_id would silently accept
-- another tenant's document. ON DELETE CASCADE, the extraction_field_results shape rather
-- than extraction_jobs' RESTRICT — a page image is a derived, regenerable copy with no
-- meaning once its document is gone. The purge still deletes leaf-first so its per-table
-- counts stay honest.
--
-- No updated_at and no trigger: a re-render arrives as a whole-set replace, DELETE then
-- INSERT, the workflow_role_members shape.

-- +goose Up
CREATE TABLE extraction_page_images (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id uuid        NOT NULL,
    page_number int         NOT NULL CHECK (page_number >= 1),
    -- The render's own dimensions, never pageWidthPt * dpi / 72: the renderer rounds up, so
    -- US-Letter at DPI 150 is 1651 rows and not 1650. A canvas scales a normalised box by
    -- these, so a recomputed value misplaces every highlight.
    width_px    int         NOT NULL CHECK (width_px > 0),
    height_px   int         NOT NULL CHECK (height_px > 0),
    -- Where the PNG landed. Stored rather than derived for documents.storage_key's reason:
    -- the object is written first and the row records what happened, so a later key-scheme
    -- change leaves already-written rows resolvable.
    storage_key text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT extraction_page_images_tenant_id_id_uq UNIQUE (tenant_id, id),
    CONSTRAINT extraction_page_images_tenant_document_fk
        FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, id) ON DELETE CASCADE,
    -- Row-level security protects the row; this protects what the row points at. A key
    -- outside this row's own tenant prefix is refused, so no page-image row can address
    -- another tenant's objects even if every policy were dropped. starts_with, not LIKE:
    -- no pattern metacharacters to reason about.
    CONSTRAINT extraction_page_images_key_tenant_scoped
        CHECK (starts_with(storage_key, 'tenants/' || tenant_id::text || '/'))
);

-- One row per page of a document, and the read path's own lookup: every page of a document
-- in page order. Leading with tenant_id keeps the plan per-tenant, and its (tenant_id,
-- document_id) prefix services the cascade check from documents.
CREATE UNIQUE INDEX extraction_page_images_tenant_document_page_uq
    ON extraction_page_images (tenant_id, document_id, page_number);

-- Force is what subjects the table owner (the migrator) to the policy below; enable alone
-- would let it bypass (docs/migrations.md section 1).
ALTER TABLE extraction_page_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_page_images FORCE  ROW LEVEL SECURITY;

-- No TO clause, so the policy binds every role. The USING doubles as the INSERT WITH CHECK;
-- an unset GUC yields NULL and therefore no rows.
CREATE POLICY tenant_isolation ON extraction_page_images
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- No UPDATE: a re-render arrives as a whole-set replace, i.e. DELETE then INSERT
-- (workflow_role_members). Least privilege (docs/migrations.md section 3). Nothing to
-- invoice_tenant_reader — it enumerates documents cross-tenant and holds no object-storage
-- credential, so page-image keys buy it nothing.
GRANT SELECT, INSERT, DELETE ON extraction_page_images TO invoice_app;

-- +goose Down
DROP TABLE extraction_page_images;
