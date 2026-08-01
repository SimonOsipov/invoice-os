-- +goose Up
-- BULK-01-01: the source filename the batch was imported from.
-- 20260714100953_import_batches.sql:23-24 recorded a deliberate deferral --
-- "Deliberately NO source-filename / completed_at column (minimal; deferred to
-- M4-03/M4-11 if a real consumer needs them)". BULK-01 is that consumer: with
-- several batches on one review screen, "which file did this row come from?"
-- is otherwise unanswerable.
--
-- NULLABLE, no default: batches imported before this migration read NULL, which
-- the wire carries as an explicit JSON null and the UI renders as "source not
-- recorded". NOT `NOT NULL DEFAULT ''` -- '' would make an unrecorded source
-- indistinguishable from a file named nothing, and this codebase's convention is
-- absent-means-absent.
--
-- NO CHECK constraint: the value is caller content, not a system-written counter,
-- so the store-invalid-faithfully rule applies. Length/encoding hygiene is Go's
-- job (sanitizeFilename), where a bad name is coerced rather than 22021'ing.
--
-- NO RLS or policy work: this adds a COLUMN to a table that is already born with
-- ENABLE + FORCE ROW LEVEL SECURITY and the `tenant_isolation` policy
-- (20260714100953_import_batches.sql:46-55). A policy is per-ROW, not per-column,
-- so the new column is covered the moment it exists.
--
-- NO GRANT: `GRANT SELECT, INSERT, UPDATE ON import_batches TO invoice_app`
-- (same file, :64) is table-level and already covers new columns.
ALTER TABLE import_batches ADD COLUMN filename text;

-- +goose Down
ALTER TABLE import_batches DROP COLUMN filename;
