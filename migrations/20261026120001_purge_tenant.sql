-- ceiling: any invoice_app service can purge the tenant it binds; revisit if invoice_app stops being trusted code.
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION public.purge_tenant(p_tenant_id uuid) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' SET lock_timeout = '5s' AS $$
DECLARE
    v_removed integer;
BEGIN
    IF p_tenant_id IS NULL
       OR p_tenant_id IS DISTINCT FROM nullif(pg_catalog.current_setting('app.current_tenant', true), '')::uuid THEN
        RAISE EXCEPTION 'purge_tenant: p_tenant_id does not match app.current_tenant'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF EXISTS (SELECT 1 FROM public.house_company WHERE tenant_id = p_tenant_id) THEN
        RAISE EXCEPTION 'purge_tenant: the house tenant is never purged'
            USING ERRCODE = 'insufficient_privilege';
    END IF;

    DELETE FROM public.idempotency_keys WHERE tenant_id = p_tenant_id;
    DELETE FROM public.river_job WHERE args @> pg_catalog.jsonb_build_object('tenant_id', p_tenant_id::text);

    -- Cloud SQL refuses session_replication_role. Each DISABLE locks its table against writers until commit.
    ALTER TABLE public.approval_policy_versions DISABLE TRIGGER approval_policy_versions_seal_guard;
    ALTER TABLE public.approval_policy_steps DISABLE TRIGGER approval_policy_steps_content_lock;
    DELETE FROM public.tenants WHERE id = p_tenant_id;
    GET DIAGNOSTICS v_removed = ROW_COUNT;
    ALTER TABLE public.approval_policy_steps ENABLE TRIGGER approval_policy_steps_content_lock;
    ALTER TABLE public.approval_policy_versions ENABLE TRIGGER approval_policy_versions_seal_guard;

    -- Last: this lock stops every tenant's audit writes until the caller commits.
    ALTER TABLE public.audit_log DISABLE TRIGGER audit_log_no_update_delete;
    DELETE FROM public.audit_log WHERE tenant_id = p_tenant_id;
    ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update_delete;

    RETURN v_removed;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.purge_tenant(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.purge_tenant(uuid) TO invoice_app;

-- +goose Down
DROP FUNCTION public.purge_tenant(uuid);
