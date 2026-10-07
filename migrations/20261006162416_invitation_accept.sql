-- auth_hook_reader owns the token lookup: it is the only role with a cross-tenant read policy.
-- accept_invitation checks the GUC before the lock, as provision_workspace does, so a wrong GUC learns nothing.
-- ceiling: invoice_app names one invite per known token across tenants; revisit if invoice_app stops being trusted code.
-- ceiling: provision_workspace and accept_invitation are the two membership writers behind the identity lock; revisit when a third appears.

-- +goose Up
GRANT SELECT (id, tenant_id, role, invitee_email, status, expires_at, token_hash) ON public.invitations TO auth_hook_reader;
GRANT SELECT (id, name) ON public.tenants TO auth_hook_reader;
CREATE POLICY invitation_token_lookup ON public.invitations FOR SELECT TO auth_hook_reader USING (true);
CREATE POLICY invitation_workspace_lookup ON public.tenants FOR SELECT TO auth_hook_reader USING (true);

-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.invitation_by_token(p_token text)
RETURNS TABLE (invitation_id uuid, tenant_id uuid, workspace text, role text, email text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT i.id, i.tenant_id, t.name, i.role, i.invitee_email
      FROM public.invitations i
      JOIN public.tenants t ON t.id = i.tenant_id
     WHERE i.token_hash = pg_catalog.sha256(pg_catalog.convert_to(p_token, 'UTF8'))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.invitation_by_token(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.invitation_by_token(text) TO invoice_app;
RESET ROLE;

-- +goose StatementBegin
CREATE FUNCTION public.accept_invitation(p_tenant_id uuid, p_token text, p_user_id uuid, p_email text)
RETURNS TABLE (invitation_id uuid, role text)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    v_id uuid;
    v_role text;
    v_email text;
BEGIN
    -- Before the lock, so a mismatched or unset GUC learns nothing.
    IF p_tenant_id IS DISTINCT FROM nullif(current_setting('app.current_tenant', true), '')::uuid THEN
        RAISE EXCEPTION 'accept_invitation: p_tenant_id does not match app.current_tenant (row-level security)'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- provision_workspace's key: accept and provision serialise per identity.
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended(p_user_id::text, 0));
    SELECT i.id, i.role, i.invitee_email INTO v_id, v_role, v_email
      FROM public.invitations i
     WHERE i.token_hash = pg_catalog.sha256(pg_catalog.convert_to(p_token, 'UTF8'))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
       FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invitation is not valid'
            USING ERRCODE = 'no_data_found', CONSTRAINT = 'invitation_not_valid';
    END IF;
    IF public.identity_has_membership(p_user_id) THEN
        RAISE EXCEPTION 'identity already holds a membership'
            USING ERRCODE = 'unique_violation', CONSTRAINT = 'one_workspace_per_identity';
    END IF;
    IF pg_catalog.lower(v_email) IS DISTINCT FROM pg_catalog.lower(pg_catalog.btrim(p_email)) THEN
        RAISE EXCEPTION 'invitation is for another address'
            USING ERRCODE = 'raise_exception', CONSTRAINT = 'invitation_email_mismatch';
    END IF;
    INSERT INTO public.memberships (tenant_id, user_id, role, status, display_name, email)
    VALUES (p_tenant_id, p_user_id, v_role, 'active', NULL, v_email);
    UPDATE public.invitations SET status = 'accepted' WHERE id = v_id;
    RETURN QUERY SELECT v_id, v_role;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.accept_invitation(uuid, text, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.accept_invitation(uuid, text, uuid, text) TO invoice_app;

-- +goose Down
DROP FUNCTION IF EXISTS public.accept_invitation(uuid, text, uuid, text);
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION IF EXISTS public.invitation_by_token(text);
RESET ROLE;
DROP POLICY IF EXISTS invitation_workspace_lookup ON public.tenants;
DROP POLICY IF EXISTS invitation_token_lookup ON public.invitations;
REVOKE SELECT ON public.tenants FROM auth_hook_reader;
REVOKE SELECT ON public.invitations FROM auth_hook_reader;
