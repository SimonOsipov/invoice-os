-- +goose Up
ALTER TABLE public.tenants
    ADD COLUMN deletion_scheduled_at timestamptz,
    ADD COLUMN deletion_due_at       timestamptz,
    ADD COLUMN deletion_scheduled_by uuid,
    ADD CONSTRAINT tenants_deletion_all_or_none CHECK (
        (deletion_scheduled_at IS NULL) = (deletion_due_at IS NULL)
        AND (deletion_scheduled_at IS NULL) = (deletion_scheduled_by IS NULL));

-- The due time is derived here, in UTC, so every writer gets the same 30 days.
-- +goose StatementBegin
CREATE FUNCTION public.tenants_deletion_guard() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' SET "TimeZone" = 'UTC' AS $$
BEGIN
    IF NEW.deletion_scheduled_at IS NOT NULL
       AND EXISTS (SELECT 1 FROM public.house_company WHERE tenant_id = NEW.id) THEN
        RAISE EXCEPTION 'the house tenant is never deleted'
            USING ERRCODE = 'insufficient_privilege', CONSTRAINT = 'house_tenant_not_deletable';
    END IF;
    NEW.deletion_due_at := NEW.deletion_scheduled_at + interval '30 days';
    RETURN NEW;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.tenants_deletion_guard() FROM PUBLIC;
CREATE TRIGGER tenants_deletion_guard
    BEFORE INSERT OR UPDATE OF deletion_scheduled_at, deletion_due_at ON public.tenants
    FOR EACH ROW EXECUTE FUNCTION public.tenants_deletion_guard();
GRANT UPDATE (deletion_scheduled_at, deletion_scheduled_by) ON public.tenants TO invoice_app;

-- An invite of a workspace scheduled for deletion reads as not pending.
GRANT SELECT (deletion_due_at) ON public.tenants TO auth_hook_reader;
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.invitation_by_token(p_token text)
RETURNS TABLE (invitation_id uuid, tenant_id uuid, workspace text, role text, email text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT i.id, i.tenant_id, t.name, i.role, i.invitee_email
      FROM public.invitations i
      JOIN public.tenants t ON t.id = i.tenant_id
     WHERE i.token_hash = pg_catalog.sha256(pg_catalog.convert_to(p_token, 'UTF8'))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
       AND t.deletion_due_at IS NULL
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
       AND t.deletion_due_at IS NULL
       AND NOT EXISTS (SELECT 1 FROM public.memberships x WHERE x.user_id = p_user_id AND x.status <> 'removed')
     ORDER BY i.expires_at, i.id
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.invitation_pending_for_email(p_email text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.invitations i
     WHERE pg_catalog.lower(i.invitee_email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
       AND NOT EXISTS (SELECT 1 FROM public.tenants t WHERE t.id = i.tenant_id AND t.deletion_due_at IS NOT NULL))
$$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.invitation_by_token(p_token text)
RETURNS TABLE (invitation_id uuid, tenant_id uuid, workspace text, role text, email text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT i.id, i.tenant_id, t.name, i.role, i.invitee_email
      FROM public.invitations i
      JOIN public.tenants t ON t.id = i.tenant_id
     WHERE i.token_hash = pg_catalog.sha256(pg_catalog.convert_to(p_token, 'UTF8'))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now()
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
CREATE OR REPLACE FUNCTION public.invitation_pending_for_email(p_email text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.invitations i
     WHERE pg_catalog.lower(i.invitee_email) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND i.status = 'pending' AND i.expires_at > pg_catalog.now())
$$;
-- +goose StatementEnd
RESET ROLE;
REVOKE SELECT (deletion_due_at) ON public.tenants FROM auth_hook_reader;
REVOKE UPDATE (deletion_scheduled_at, deletion_scheduled_by) ON public.tenants FROM invoice_app;
DROP TRIGGER tenants_deletion_guard ON public.tenants;
DROP FUNCTION public.tenants_deletion_guard();
ALTER TABLE public.tenants
    DROP CONSTRAINT tenants_deletion_all_or_none,
    DROP COLUMN deletion_scheduled_by,
    DROP COLUMN deletion_due_at,
    DROP COLUMN deletion_scheduled_at;
