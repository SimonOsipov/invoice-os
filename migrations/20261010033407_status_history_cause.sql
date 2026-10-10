-- +goose Up
ALTER TABLE invoice_status_history
    ADD COLUMN cause_rule_set_version_id uuid REFERENCES rule_set_versions(id);

-- +goose Down
ALTER TABLE invoice_status_history DROP COLUMN cause_rule_set_version_id;
