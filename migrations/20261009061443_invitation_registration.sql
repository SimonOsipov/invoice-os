-- auth_hook_reader owns the lookup: it already holds the cross-tenant read policy on invitations.
-- ceiling: a sequential scan of invitations per register call, index lower(invitee_email) above ~10k rows.

-- +goose Up
ALTER TABLE public.invitations ADD COLUMN registered_token_hash bytea
    CONSTRAINT invitations_registered_token_hash_len CHECK (registered_token_hash IS NULL OR octet_length(registered_token_hash) = 32);

-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.invitation_pending_for_email(p_email text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.invitations i
     WHERE pg_catalog.lower(i.invitee_email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now())
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.invitation_pending_for_email(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.invitation_pending_for_email(text) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION IF EXISTS public.invitation_pending_for_email(text);
RESET ROLE;
ALTER TABLE public.invitations DROP COLUMN registered_token_hash;
