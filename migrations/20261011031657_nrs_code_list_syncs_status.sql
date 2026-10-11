-- +goose Up
-- held: the shrink guard refused the pull. released: an operator lets the next sync apply it.
ALTER TABLE nrs_code_list_syncs
    ADD COLUMN status text NOT NULL DEFAULT 'applied' CHECK (status IN ('applied', 'held', 'released'));
-- Column-level, so the row contents stay unreadable to the app.
GRANT SELECT (id, list, synced_at, status) ON nrs_code_list_syncs TO invoice_app;

-- +goose Down
REVOKE SELECT (id, list, synced_at, status) ON nrs_code_list_syncs FROM invoice_app;
ALTER TABLE nrs_code_list_syncs DROP COLUMN status;
