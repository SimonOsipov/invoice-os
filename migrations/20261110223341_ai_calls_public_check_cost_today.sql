-- The worker reads the check's AI spend for the UTC day without a tenant or a direct read of ai_calls.
-- +goose Up
GRANT SELECT (tenant_id, cost, created_at) ON public.ai_calls TO auth_hook_reader;
CREATE POLICY public_check_spend ON public.ai_calls FOR SELECT TO auth_hook_reader USING (tenant_id IS NULL);
CREATE INDEX ai_calls_public_check_day ON public.ai_calls (created_at) WHERE tenant_id IS NULL;

SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.public_check_ai_cost_today() RETURNS numeric
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT coalesce(sum(c.cost), 0) FROM public.ai_calls c
     WHERE c.tenant_id IS NULL AND c.created_at >= date_trunc('day', now(), 'UTC')
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.public_check_ai_cost_today() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.public_check_ai_cost_today() TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.public_check_ai_cost_today();
RESET ROLE;
DROP INDEX public.ai_calls_public_check_day;
DROP POLICY public_check_spend ON public.ai_calls;
REVOKE SELECT (tenant_id, cost, created_at) ON public.ai_calls FROM auth_hook_reader;
