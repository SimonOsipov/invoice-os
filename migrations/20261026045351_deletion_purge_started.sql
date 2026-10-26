-- ceiling: any invoice_app service can start the purge of the tenant it binds, which ends its Cancel.
-- +goose Up
ALTER TABLE public.tenants
    ADD COLUMN deletion_purge_started_at timestamptz,
    ADD CONSTRAINT tenants_purge_started_needs_schedule CHECK (
        deletion_purge_started_at IS NULL OR deletion_scheduled_at IS NOT NULL);

-- +goose StatementBegin
CREATE FUNCTION public.tenants_purge_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = '' AS $$
BEGIN
    IF OLD.deletion_purge_started_at IS NOT NULL
       AND (NEW.deletion_purge_started_at IS DISTINCT FROM OLD.deletion_purge_started_at
            OR NEW.deletion_scheduled_at IS DISTINCT FROM OLD.deletion_scheduled_at) THEN
        RAISE EXCEPTION 'the purge has started; the deletion can no longer change'
            USING ERRCODE = 'insufficient_privilege', CONSTRAINT = 'tenant_deletion_underway';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.tenants_purge_guard() FROM PUBLIC;
CREATE TRIGGER tenants_purge_guard
    BEFORE UPDATE OF deletion_scheduled_at, deletion_purge_started_at ON public.tenants
    FOR EACH ROW EXECUTE FUNCTION public.tenants_purge_guard();
GRANT UPDATE (deletion_purge_started_at) ON public.tenants TO invoice_app;

-- +goose Down
REVOKE UPDATE (deletion_purge_started_at) ON public.tenants FROM invoice_app;
DROP TRIGGER tenants_purge_guard ON public.tenants;
DROP FUNCTION public.tenants_purge_guard();
ALTER TABLE public.tenants
    DROP CONSTRAINT tenants_purge_started_needs_schedule,
    DROP COLUMN deletion_purge_started_at;
