-- +goose Up
-- Sign-outs every gateway instance reads before it trusts a cached live session. Global: no tenant, no RLS.
CREATE TABLE gateway_session_revocations (
    subject    text        PRIMARY KEY CHECK (subject <> ''),
    revoked_at timestamptz NOT NULL
);
CREATE INDEX gateway_session_revocations_revoked_at ON gateway_session_revocations (revoked_at);
-- Per-address limit counts shared by every gateway instance. Global: no tenant, no RLS.
CREATE TABLE gateway_throttle_counts (
    scope      text        NOT NULL CHECK (scope <> ''),
    key_hash   bytea       NOT NULL CHECK (length(key_hash) = 32),  -- sha256 of the folded address; the address is never stored
    n          integer     NOT NULL CHECK (n >= 0),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (scope, key_hash)
);
CREATE INDEX gateway_throttle_counts_expires_at ON gateway_throttle_counts (expires_at);
-- The role is created by db/bootstrap.sql; a database bootstrapped before it has none.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_gateway') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON gateway_session_revocations, gateway_throttle_counts TO invoice_gateway;
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE gateway_throttle_counts;
DROP TABLE gateway_session_revocations;
