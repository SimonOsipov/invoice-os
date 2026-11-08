-- +goose Up
ALTER TABLE account_event_handoffs ADD COLUMN first_session boolean NOT NULL DEFAULT false;
-- A first session is a sign-in; the mover records its device and writes no row.
ALTER TABLE account_event_handoffs ADD CONSTRAINT account_event_handoffs_first_session_check
    CHECK (NOT first_session OR event = 'account.signed_in');

-- +goose Down
ALTER TABLE account_event_handoffs DROP COLUMN first_session;
