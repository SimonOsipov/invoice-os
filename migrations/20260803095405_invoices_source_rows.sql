-- +goose Up
-- The sheet rows of the source file that became one invoice, captured at import.
ALTER TABLE invoices ADD COLUMN source_rows integer[];

-- A range without a document is an orphan claim about a file we do not hold.
ALTER TABLE invoices
    ADD CONSTRAINT invoices_source_rows_requires_document
        CHECK (source_rows IS NULL OR source_document_id IS NOT NULL);

-- Header is sheet row 1, so a data row is always >= 2 (sheetRow(i) = i+2).
-- cardinality, not array_length: array_length('{}',1) is NULL and a CHECK that
-- evaluates to NULL is SATISFIED. IS TRUE for the same reason -- a bare
-- `2 <= ALL ('{2,NULL}')` is NULL. array_ndims keeps source_rows[1] meaningful.
ALTER TABLE invoices
    ADD CONSTRAINT invoices_source_rows_are_sheet_rows
        CHECK (source_rows IS NULL
               OR (array_ndims(source_rows) = 1
                   AND cardinality(source_rows) >= 1
                   AND (2 <= ALL (source_rows)) IS TRUE));

-- No new GRANT: invoices carries a table-level GRANT (20260714103137_invoices.sql:95)
-- covering columns added later. No policy either -- tenant_isolation is row-scoped.

-- +goose Down
ALTER TABLE invoices DROP CONSTRAINT invoices_source_rows_are_sheet_rows;
ALTER TABLE invoices DROP CONSTRAINT invoices_source_rows_requires_document;
ALTER TABLE invoices DROP COLUMN source_rows;
