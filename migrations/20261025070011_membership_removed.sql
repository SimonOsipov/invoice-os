-- A removed row keeps display_name only; identity checks ignore it and an accepted invite revives it.
-- ceiling: invoice_app learns whether a user id was removed from any workspace; revisit if invoice_app stops being trusted code.
-- ceiling: provision_workspace and every accept_invitation and accept_invitation_by_id overload are the membership writers behind the identity lock; revisit when another writer creates a row.

-- +goose Up
ALTER TABLE memberships DROP CONSTRAINT memberships_status_check,
  ADD CONSTRAINT memberships_status_check CHECK (status IN ('active','invited','suspended','removed'));

-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.identity_has_membership(p_user_id uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.memberships WHERE user_id = p_user_id AND status <> 'removed')
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.pending_invites_for_email(p_email text, p_user_id uuid)
RETURNS TABLE (invitation_id uuid, tenant_id uuid, workspace text, role text, inviter text, expires_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT i.id, i.tenant_id, t.name, i.role,
           coalesce(nullif(pg_catalog.btrim(m.display_name), ''), m.email), i.expires_at
      FROM public.invitations i
      JOIN public.tenants t ON t.id = i.tenant_id
      LEFT JOIN public.memberships m ON m.tenant_id = i.tenant_id AND m.user_id = i.invited_by
     WHERE pg_catalog.lower(i.invitee_email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
       AND NOT EXISTS (SELECT 1 FROM public.memberships x WHERE x.user_id = p_user_id AND x.status <> 'removed')
     ORDER BY i.expires_at, i.id
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.identity_has_removed_membership(p_user_id uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.memberships WHERE user_id = p_user_id AND status = 'removed')
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.identity_has_removed_membership(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.identity_has_removed_membership(uuid) TO invoice_app;
RESET ROLE;

-- +goose StatementBegin
CREATE FUNCTION public.accept_invitation(p_tenant_id uuid, p_token text, p_user_id uuid, p_email text, p_display_name text)
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
    VALUES (p_tenant_id, p_user_id, v_role, 'active', nullif(pg_catalog.btrim(p_display_name), ''), v_email)
    ON CONFLICT ON CONSTRAINT memberships_tenant_user_uq DO UPDATE
       SET role = EXCLUDED.role, status = 'active', email = EXCLUDED.email,
           display_name = COALESCE(EXCLUDED.display_name, public.memberships.display_name),
           all_clients = false, created_at = pg_catalog.now();
    UPDATE public.invitations SET status = 'accepted' WHERE id = v_id;
    RETURN QUERY SELECT v_id, v_role;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.accept_invitation(uuid, text, uuid, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.accept_invitation(uuid, text, uuid, text, text) TO invoice_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.accept_invitation(p_tenant_id uuid, p_token text, p_user_id uuid, p_email text)
RETURNS TABLE (invitation_id uuid, role text)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    RETURN QUERY SELECT * FROM public.accept_invitation(p_tenant_id, p_token, p_user_id, p_email, NULL);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.accept_invitation_by_id(p_tenant_id uuid, p_invitation_id uuid, p_user_id uuid, p_email text, p_display_name text)
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
    VALUES (p_tenant_id, p_user_id, v_role, 'active', nullif(pg_catalog.btrim(p_display_name), ''), v_email)
    ON CONFLICT ON CONSTRAINT memberships_tenant_user_uq DO UPDATE
       SET role = EXCLUDED.role, status = 'active', email = EXCLUDED.email,
           display_name = COALESCE(EXCLUDED.display_name, public.memberships.display_name),
           all_clients = false, created_at = pg_catalog.now();
    UPDATE public.invitations SET status = 'accepted' WHERE id = v_id;
    RETURN QUERY SELECT v_id, v_role;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.accept_invitation_by_id(uuid, uuid, uuid, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.accept_invitation_by_id(uuid, uuid, uuid, text, text) TO invoice_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.accept_invitation_by_id(p_tenant_id uuid, p_invitation_id uuid, p_user_id uuid, p_email text)
RETURNS TABLE (invitation_id uuid, role text)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    RETURN QUERY SELECT * FROM public.accept_invitation_by_id(p_tenant_id, p_invitation_id, p_user_id, p_email, NULL);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.accept_invitation(p_tenant_id uuid, p_token text, p_user_id uuid, p_email text)
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
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.accept_invitation_by_id(p_tenant_id uuid, p_invitation_id uuid, p_user_id uuid, p_email text)
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
DROP FUNCTION public.accept_invitation(uuid, text, uuid, text, text);
DROP FUNCTION public.accept_invitation_by_id(uuid, uuid, uuid, text, text);
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.identity_has_removed_membership(uuid);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.identity_has_membership(p_user_id uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.memberships WHERE user_id = p_user_id)
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.pending_invites_for_email(p_email text, p_user_id uuid)
RETURNS TABLE (invitation_id uuid, tenant_id uuid, workspace text, role text, inviter text, expires_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT i.id, i.tenant_id, t.name, i.role,
           coalesce(nullif(pg_catalog.btrim(m.display_name), ''), m.email), i.expires_at
      FROM public.invitations i
      JOIN public.tenants t ON t.id = i.tenant_id
      LEFT JOIN public.memberships m ON m.tenant_id = i.tenant_id AND m.user_id = i.invited_by
     WHERE pg_catalog.lower(i.invitee_email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
       AND NOT EXISTS (SELECT 1 FROM public.memberships x WHERE x.user_id = p_user_id)
     ORDER BY i.expires_at, i.id
$$;
-- +goose StatementEnd
RESET ROLE;
-- FORCE RLS hides every row from the migrator without a tenant GUC; the toggle is transactional.
ALTER TABLE memberships NO FORCE ROW LEVEL SECURITY;
UPDATE memberships SET status = 'suspended' WHERE status = 'removed';
ALTER TABLE memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE memberships DROP CONSTRAINT memberships_status_check,
  ADD CONSTRAINT memberships_status_check CHECK (status IN ('active','invited','suspended'));
