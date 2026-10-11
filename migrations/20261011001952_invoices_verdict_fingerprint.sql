-- +goose Up
-- Content fingerprint the stored verdict was evaluated against. NULL: a verdict
-- written before this column existed, never backfilled.
ALTER TABLE invoices ADD COLUMN verdict_fingerprint text;

-- +goose Down
ALTER TABLE invoices DROP COLUMN verdict_fingerprint;
