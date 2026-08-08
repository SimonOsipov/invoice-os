-- display_name/email duplicate a person per-membership across tenants, chosen over a global users table.

-- +goose Up
ALTER TABLE memberships
    ADD COLUMN status       text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','invited','suspended')),
    ADD COLUMN display_name text,
    ADD COLUMN email        text;

-- +goose Down
ALTER TABLE memberships
    DROP COLUMN status,
    DROP COLUMN display_name,
    DROP COLUMN email;
