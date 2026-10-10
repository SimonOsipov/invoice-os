-- +goose Up
ALTER TABLE rule_set_versions DROP CONSTRAINT rule_set_versions_active_is_sealed;
DROP INDEX rule_set_versions_one_active;
ALTER TABLE rule_set_versions DROP COLUMN is_active;

-- +goose Down
ALTER TABLE rule_set_versions ADD COLUMN is_active boolean NOT NULL DEFAULT false;
UPDATE rule_set_versions SET is_active = true WHERE id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date);
CREATE UNIQUE INDEX rule_set_versions_one_active ON rule_set_versions ((is_active)) WHERE is_active;
ALTER TABLE rule_set_versions ADD CONSTRAINT rule_set_versions_active_is_sealed CHECK (NOT is_active OR sealed);
