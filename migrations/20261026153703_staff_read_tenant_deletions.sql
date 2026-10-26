-- +goose Up
GRANT SELECT (deletion_due_at, deletion_purge_started_at) ON public.tenants TO staff_reader;
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_tenant_deletions(p_actor uuid)
RETURNS TABLE (tenant_id uuid, deletion_due_at timestamptz, deletion_purge_started_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_tenant_deletions: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT t.id, t.deletion_due_at, t.deletion_purge_started_at FROM public.tenants t;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_tenant_deletions(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_tenant_deletions(uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_tenant_deletions(uuid);
RESET ROLE;
REVOKE SELECT (deletion_due_at, deletion_purge_started_at) ON public.tenants FROM staff_reader;
