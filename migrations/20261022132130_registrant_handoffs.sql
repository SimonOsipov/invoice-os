-- +goose Up
-- Registrants the gateway hands to the contacts system; the worker moves each row into a River job.
-- Global like contacts: no tenant, no RLS.
CREATE TABLE registrant_handoffs (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        text        NOT NULL,
    email          text        NOT NULL,
    display_name   text        NOT NULL,
    workspace_name text        NOT NULL,
    consent_text   text        NOT NULL,  -- '' when unticked
    consent_at     text        NOT NULL,  -- RFC 3339 as GoTrue stored it; the job parses it
    created_at     timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT, DELETE ON registrant_handoffs TO invoice_app;
-- The role is created by db/bootstrap.sql; a database bootstrapped before it has none.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_gateway') THEN
    GRANT INSERT ON registrant_handoffs TO invoice_gateway;
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE registrant_handoffs;
