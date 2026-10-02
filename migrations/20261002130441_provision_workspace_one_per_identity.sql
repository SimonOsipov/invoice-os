-- Provisioning refuses an identity that holds any membership, in any tenant, in any status.
-- ceiling: only provision_workspace takes the per-identity lock; revisit when a second membership writer exists.
-- ceiling: invoice_app learns whether a user id holds a membership by provisioning under a matching GUC; revisit if invoice_app stops being trusted code.

-- +goose Up
-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.identity_has_membership(p_user_id uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.memberships WHERE user_id = p_user_id)
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.identity_has_membership(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.identity_has_membership(uuid) TO invoice_migrator;
RESET ROLE;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.provision_workspace(
    p_tenant_id uuid, p_name text, p_kind text, p_user_id uuid, p_display_name text, p_email text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    -- Before the guard, so a mismatched or unset GUC learns nothing about p_user_id.
    IF p_tenant_id IS DISTINCT FROM nullif(current_setting('app.current_tenant', true), '')::uuid THEN
        RAISE EXCEPTION 'provision_workspace: p_tenant_id does not match app.current_tenant (row-level security)'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended(p_user_id::text, 0));
    IF public.identity_has_membership(p_user_id) THEN
        RAISE EXCEPTION 'identity already holds a membership'
            USING ERRCODE = 'unique_violation', CONSTRAINT = 'one_workspace_per_identity';
    END IF;
    -- NULL kind omits the column so tenants.kind's DEFAULT applies.
    IF p_kind IS NULL THEN
        INSERT INTO public.tenants (id, name) VALUES (p_tenant_id, p_name);
    ELSE
        INSERT INTO public.tenants (id, name, kind) VALUES (p_tenant_id, p_name, p_kind);
    END IF;
    INSERT INTO public.memberships (tenant_id, user_id, role, status, display_name, email)
    VALUES (p_tenant_id, p_user_id, 'admin', 'active', p_display_name, p_email);
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.provision_workspace(
    p_tenant_id uuid, p_name text, p_kind text, p_user_id uuid, p_display_name text, p_email text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    -- NULL kind omits the column so tenants.kind's DEFAULT applies.
    IF p_kind IS NULL THEN
        INSERT INTO public.tenants (id, name) VALUES (p_tenant_id, p_name);
    ELSE
        INSERT INTO public.tenants (id, name, kind) VALUES (p_tenant_id, p_name, p_kind);
    END IF;
    INSERT INTO public.memberships (tenant_id, user_id, role, status, display_name, email)
    VALUES (p_tenant_id, p_user_id, 'admin', 'active', p_display_name, p_email);
END
$$;
-- +goose StatementEnd

SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.identity_has_membership(uuid);
RESET ROLE;
