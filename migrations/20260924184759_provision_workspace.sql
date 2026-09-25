-- invoice_app creates a tenant only with its first active admin, through this DEFINER; it stays SELECT-only on tenants.
-- The migrator owner is NOBYPASSRLS and FORCE binds it, so tenant_isolation still checks the GUC. Asserted by tenants_provision_rls_test.go.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION public.provision_workspace(
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

REVOKE EXECUTE ON FUNCTION public.provision_workspace(uuid, text, text, uuid, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.provision_workspace(uuid, text, text, uuid, text, text) TO invoice_app;

-- +goose Down
DROP FUNCTION public.provision_workspace(uuid, text, text, uuid, text, text);
