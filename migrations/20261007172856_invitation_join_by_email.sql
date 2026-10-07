-- pending_invites_for_email is owned by auth_hook_reader like invitation_by_token; accept_invitation_by_id by the migrator like accept_invitation.
-- ceiling: a sequential scan of invitations per call; index lower(invitee_email) above ~10k rows.

-- +goose Up
GRANT SELECT (invited_by) ON public.invitations TO auth_hook_reader;
GRANT SELECT (display_name, email) ON public.memberships TO auth_hook_reader;

-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.pending_invites_for_email(p_email text)
RETURNS TABLE (invitation_id uuid, tenant_id uuid, workspace text, role text, inviter text, expires_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT i.id, i.tenant_id, t.name, i.role,
           coalesce(nullif(pg_catalog.btrim(m.display_name), ''), m.email), i.expires_at
      FROM public.invitations i
      JOIN public.tenants t ON t.id = i.tenant_id
      LEFT JOIN public.memberships m ON m.tenant_id = i.tenant_id AND m.user_id = i.invited_by
     WHERE pg_catalog.lower(i.invitee_email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
     ORDER BY i.expires_at, i.id
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.pending_invites_for_email(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.pending_invites_for_email(text) TO invoice_app;
RESET ROLE;

-- +goose StatementBegin
CREATE FUNCTION public.accept_invitation_by_id(p_tenant_id uuid, p_invitation_id uuid, p_user_id uuid, p_email text)
RETURNS TABLE (invitation_id uuid, role text)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    v_id uuid;
    v_role text;
    v_email text;
BEGIN
    -- Before the lock, so a mismatched or unset GUC learns nothing.
    IF p_tenant_id IS DISTINCT FROM nullif(current_setting('app.current_tenant', true), '')::uuid THEN
        RAISE EXCEPTION 'accept_invitation_by_id: p_tenant_id does not match app.current_tenant (row-level security)'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- provision_workspace's key: accept and provision serialise per identity.
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended(p_user_id::text, 0));
    SELECT i.id, i.role, i.invitee_email INTO v_id, v_role, v_email
      FROM public.invitations i
     WHERE i.id = p_invitation_id AND i.tenant_id = p_tenant_id
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
       AND pg_catalog.lower(i.invitee_email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invitation is not valid'
            USING ERRCODE = 'no_data_found', CONSTRAINT = 'invitation_not_valid';
    END IF;
    IF public.identity_has_membership(p_user_id) THEN
        RAISE EXCEPTION 'identity already holds a membership'
            USING ERRCODE = 'unique_violation', CONSTRAINT = 'one_workspace_per_identity';
    END IF;
    INSERT INTO public.memberships (tenant_id, user_id, role, status, display_name, email)
    VALUES (p_tenant_id, p_user_id, v_role, 'active', NULL, v_email);
    UPDATE public.invitations SET status = 'accepted' WHERE id = v_id;
    RETURN QUERY SELECT v_id, v_role;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.accept_invitation_by_id(uuid, uuid, uuid, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.accept_invitation_by_id(uuid, uuid, uuid, text) TO invoice_app;

-- +goose Down
DROP FUNCTION IF EXISTS public.accept_invitation_by_id(uuid, uuid, uuid, text);
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION IF EXISTS public.pending_invites_for_email(text);
RESET ROLE;
REVOKE SELECT (display_name, email) ON public.memberships FROM auth_hook_reader;
REVOKE SELECT (invited_by) ON public.invitations FROM auth_hook_reader;
