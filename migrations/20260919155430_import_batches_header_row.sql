-- The 1-based file row an import read its column names from. NULL for a document
-- import and for every batch imported before this column (all of which read row 1).
-- The table-level grant and the tenant_isolation policy already cover a new column.

-- +goose Up
ALTER TABLE import_batches ADD COLUMN header_row integer
    CHECK (header_row IS NULL OR header_row >= 1);

-- +goose Down
ALTER TABLE import_batches DROP COLUMN header_row;
