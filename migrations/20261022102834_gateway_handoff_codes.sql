-- +goose Up
-- Sign-in hand-off codes, shared by every gateway instance. Global: no tenant, no RLS.
CREATE TABLE gateway_handoff_codes (
    code_hash  bytea       PRIMARY KEY CHECK (length(code_hash) = 32),  -- sha256(code); the code is never stored
    state_hash bytea       NOT NULL CHECK (length(state_hash) = 32),
    answer     text        NOT NULL,                                     -- the exchange answer JSON
    expires_at timestamptz NOT NULL
);
CREATE INDEX gateway_handoff_codes_expires_at ON gateway_handoff_codes (expires_at);
-- The role is created by db/bootstrap.sql; a database bootstrapped before it has none.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_gateway') THEN
    GRANT SELECT, INSERT, DELETE ON gateway_handoff_codes TO invoice_gateway;
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE gateway_handoff_codes;
