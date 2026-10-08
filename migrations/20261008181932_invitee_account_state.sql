-- auth_hook_reader owns it and holds a three-column read of auth.users (db.GrantAccountStateRead).
-- plpgsql: the body is not resolved at CREATE, so this applies where GoTrue has not run.

-- +goose Up
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.invitee_account_state(p_email text) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
DECLARE v_confirmed boolean;
BEGIN
    SELECT u.email_confirmed_at IS NOT NULL INTO v_confirmed
      FROM auth.users u
     WHERE pg_catalog.lower(u.email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND NOT u.is_sso_user;
    IF NOT FOUND THEN RETURN 'none'; END IF;
    RETURN CASE WHEN v_confirmed THEN 'confirmed' ELSE 'unconfirmed' END;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.invitee_account_state(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.invitee_account_state(text) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.invitee_account_state(text);
RESET ROLE;
