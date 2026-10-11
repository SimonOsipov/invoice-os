-- +goose Up
GRANT SELECT ON nrs_code_list_syncs TO invoice_app;

-- +goose Down
REVOKE SELECT ON nrs_code_list_syncs FROM invoice_app;
