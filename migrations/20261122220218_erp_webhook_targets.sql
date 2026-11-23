-- ceiling: invoice_app can list the connection ids of one vendor company key across tenants; revisit if invoice_app stops being trusted code.

-- +goose Up
GRANT SELECT (id, tenant_id, system, external_key, status) ON public.erp_connections TO auth_hook_reader;
CREATE POLICY erp_connection_webhook_lookup ON public.erp_connections FOR SELECT TO auth_hook_reader USING (true);
CREATE INDEX erp_connections_webhook_idx ON public.erp_connections (system, external_key) WHERE status = 'active';

-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.erp_webhook_targets(p_system text, p_external_key text)
RETURNS TABLE (tenant_id uuid, connection_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT c.tenant_id, c.id
      FROM public.erp_connections c
     WHERE c.system = p_system AND c.external_key = p_external_key AND c.status = 'active'
     ORDER BY c.tenant_id, c.id
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.erp_webhook_targets(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.erp_webhook_targets(text, text) TO invoice_app;
RESET ROLE;

-- +goose Down
-- Owner drops it: the migrator holds auth_hook_reader WITH INHERIT FALSE.
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION IF EXISTS public.erp_webhook_targets(text, text);
RESET ROLE;
DROP INDEX IF EXISTS public.erp_connections_webhook_idx;
DROP POLICY IF EXISTS erp_connection_webhook_lookup ON public.erp_connections;
REVOKE SELECT (id, tenant_id, system, external_key, status) ON public.erp_connections FROM auth_hook_reader;
