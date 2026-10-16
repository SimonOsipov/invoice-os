-- +goose Up
ALTER TABLE customers
    ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived'));

-- +goose Down
ALTER TABLE customers DROP COLUMN status;
