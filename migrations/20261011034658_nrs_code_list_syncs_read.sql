-- +goose Up
-- Completes the app's read of the sync log; id, list, synced_at, status are granted by the status migration.
GRANT SELECT (entry_count, added, removed, changed) ON nrs_code_list_syncs TO invoice_app;

-- +goose Down
REVOKE SELECT (entry_count, added, removed, changed) ON nrs_code_list_syncs FROM invoice_app;
