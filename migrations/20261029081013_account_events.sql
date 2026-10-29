-- ceiling: invoice_app learns which workspaces an address is an active member of; revisit if invoice_app stops being trusted code.

-- +goose Up
-- Account events the gateway hands to the worker; the worker moves each row into River jobs.
-- Global like registrant_handoffs: no tenant, no RLS.
CREATE TABLE account_event_handoffs (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    event       text        NOT NULL CHECK (event IN (
        'account.signed_in', 'account.sign_in_failed', 'account.sign_in_blocked',
        'account.signed_out', 'account.password_reset_requested', 'account.password_reset_completed')),
    user_id     uuid,
    email       text,
    ip          text        NOT NULL DEFAULT '',
    user_agent  text        NOT NULL DEFAULT '' CHECK (char_length(user_agent) <= 512),
    occurred_at timestamptz NOT NULL DEFAULT now(),
    CHECK (user_id IS NOT NULL OR email IS NOT NULL)
);
CREATE INDEX account_event_handoffs_occurred_at_idx ON account_event_handoffs (occurred_at);
GRANT SELECT, DELETE ON account_event_handoffs TO invoice_app;
-- The role is created by db/bootstrap.sql; a database bootstrapped before it has none.
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'invoice_gateway') THEN
    GRANT INSERT ON account_event_handoffs TO invoice_gateway;
  END IF;
END
$$;
-- +goose StatementEnd

-- Browser and OS pairs each account has signed in with. DELETE is for the erase sweep.
CREATE TABLE account_known_devices (
    user_id       uuid        NOT NULL,
    browser       text        NOT NULL,
    os            text        NOT NULL,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, browser, os)
);
GRANT SELECT, INSERT, DELETE ON account_known_devices TO invoice_app;

-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.account_event_memberships(p_user_id uuid, p_email text)
RETURNS TABLE (tenant_id uuid, user_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT m.tenant_id, m.user_id
      FROM public.memberships m
     WHERE m.status = 'active'
       AND CASE WHEN p_user_id IS NOT NULL THEN m.user_id = p_user_id
                ELSE pg_catalog.btrim(p_email) <> ''
                     AND pg_catalog.lower(m.email) = pg_catalog.lower(pg_catalog.btrim(p_email)) END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.account_event_memberships(uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.account_event_memberships(uuid, text) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.account_event_memberships(uuid, text);
RESET ROLE;
DROP TABLE account_known_devices;
DROP TABLE account_event_handoffs;
