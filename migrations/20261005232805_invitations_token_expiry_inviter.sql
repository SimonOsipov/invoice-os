-- The pending CHECK is NOT VALID: a legacy pending row has no token and would fail a validated
-- constraint, and a migration cannot revoke it under FORCE RLS. New and updated rows are enforced.

-- +goose Up
ALTER TABLE invitations
    ADD COLUMN token_hash  bytea,
    ADD COLUMN expires_at  timestamptz,
    ADD COLUMN invited_by  uuid,
    ADD COLUMN send_status text NOT NULL DEFAULT 'sending' CHECK (send_status IN ('sending', 'sent', 'failed'));

ALTER TABLE invitations
    ADD CONSTRAINT invitations_pending_has_token
        CHECK (status <> 'pending' OR (token_hash IS NOT NULL AND expires_at IS NOT NULL AND invited_by IS NOT NULL)) NOT VALID,
    ADD CONSTRAINT invitations_token_hash_len
        CHECK (token_hash IS NULL OR octet_length(token_hash) = 32);

CREATE UNIQUE INDEX invitations_token_hash_uq ON invitations (token_hash);

-- +goose Down
DROP INDEX invitations_token_hash_uq;

ALTER TABLE invitations
    DROP CONSTRAINT invitations_token_hash_len,
    DROP CONSTRAINT invitations_pending_has_token;

ALTER TABLE invitations
    DROP COLUMN send_status,
    DROP COLUMN invited_by,
    DROP COLUMN expires_at,
    DROP COLUMN token_hash;
