-- Staff read ai_calls across tenants through staff_read_ definer functions; two indexes serve the list and the totals.
-- +goose Up
GRANT SELECT (id, tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome, created_at) ON public.ai_calls TO staff_reader;
CREATE POLICY staff_read_lookup ON public.ai_calls FOR SELECT TO staff_reader USING (true);
CREATE INDEX ai_calls_created_idx ON public.ai_calls (created_at DESC, id DESC);
CREATE INDEX ai_calls_tenant_created_idx ON public.ai_calls (tenant_id, created_at DESC, id DESC);

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_ai_calls(p_actor uuid, p_tenant uuid, p_purpose text, p_outcome text, p_failed boolean,
    p_from timestamptz, p_to timestamptz, p_before_at timestamptz, p_before_id uuid, p_limit int)
RETURNS TABLE (id uuid, tenant_id uuid, tenant_name text, created_at timestamptz, client text, purpose text, model text,
               input_tokens bigint, output_tokens bigint, cost numeric, latency_ms bigint, attempts int, outcome text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    q text := 'SELECT c.id, c.tenant_id, t.name, c.created_at, c.client, c.purpose, c.model,
                      c.input_tokens, c.output_tokens, c.cost, c.latency_ms, c.attempts, c.outcome
                 FROM public.ai_calls c JOIN public.tenants t ON t.id = c.tenant_id
                WHERE true';
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_ai_calls: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- Built per call so each filter set is planned with its own values.
    IF p_tenant IS NOT NULL THEN q := q || ' AND c.tenant_id = $1'; END IF;
    IF p_purpose IS NOT NULL THEN q := q || ' AND c.purpose = $2'; END IF;
    IF p_outcome IS NOT NULL THEN q := q || ' AND c.outcome = $3'; END IF;
    IF p_failed THEN q := q || ' AND c.outcome <> ''ok'''; END IF;
    IF p_from IS NOT NULL THEN q := q || ' AND c.created_at >= $4'; END IF;
    IF p_to IS NOT NULL THEN q := q || ' AND c.created_at <= $5'; END IF;
    IF p_before_at IS NOT NULL THEN q := q || ' AND (c.created_at, c.id) < ($6, $7)'; END IF;
    RETURN QUERY EXECUTE q || ' ORDER BY c.created_at DESC, c.id DESC LIMIT least(coalesce($8, 101), 101)'
        USING p_tenant, p_purpose, p_outcome, p_from, p_to, p_before_at, p_before_id, p_limit;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_ai_call_totals(p_actor uuid, p_tenant uuid, p_purpose text, p_outcome text,
    p_from timestamptz, p_to timestamptz, p_before_day date, p_before_name text, p_before_tenant uuid, p_limit int)
RETURNS TABLE (day date, tenant_id uuid, tenant_name text, calls bigint, input_tokens bigint, output_tokens bigint, cost numeric, failed bigint)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    q text := 'SELECT (c.created_at AT TIME ZONE ''UTC'')::date AS day, c.tenant_id, t.name AS tenant_name, count(*) AS calls,
                      sum(c.input_tokens)::bigint AS input_tokens, sum(c.output_tokens)::bigint AS output_tokens, sum(c.cost) AS cost,
                      count(*) FILTER (WHERE c.outcome <> ''ok'') AS failed
                 FROM public.ai_calls c JOIN public.tenants t ON t.id = c.tenant_id
                WHERE true';
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_ai_call_totals: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_tenant IS NOT NULL THEN q := q || ' AND c.tenant_id = $1'; END IF;
    IF p_purpose IS NOT NULL THEN q := q || ' AND c.purpose = $2'; END IF;
    IF p_outcome IS NOT NULL THEN q := q || ' AND c.outcome = $3'; END IF;
    IF p_from IS NOT NULL THEN q := q || ' AND c.created_at >= $4'; END IF;
    IF p_to IS NOT NULL THEN q := q || ' AND c.created_at <= $5'; END IF;
    -- The keyset applies to the groups, so it sits outside the GROUP BY.
    q := 'SELECT g.day, g.tenant_id, g.tenant_name, g.calls, g.input_tokens, g.output_tokens, g.cost, g.failed FROM ('
         || q || ' GROUP BY 1, c.tenant_id, t.name) g WHERE true';
    IF p_before_day IS NOT NULL THEN
        q := q || ' AND (g.day < $6 OR (g.day = $6 AND (g.tenant_name, g.tenant_id) > ($7, $8)))';
    END IF;
    RETURN QUERY EXECUTE q || ' ORDER BY g.day DESC, g.tenant_name, g.tenant_id LIMIT least(coalesce($9, 101), 101)'
        USING p_tenant, p_purpose, p_outcome, p_from, p_to, p_before_day, p_before_name, p_before_tenant, p_limit;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_ai_calls(uuid, uuid, text, text, boolean, timestamptz, timestamptz, timestamptz, uuid, int) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.staff_read_ai_call_totals(uuid, uuid, text, text, timestamptz, timestamptz, date, text, uuid, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_ai_calls(uuid, uuid, text, text, boolean, timestamptz, timestamptz, timestamptz, uuid, int) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.staff_read_ai_call_totals(uuid, uuid, text, text, timestamptz, timestamptz, date, text, uuid, int) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_ai_call_totals(uuid, uuid, text, text, timestamptz, timestamptz, date, text, uuid, int);
DROP FUNCTION public.staff_read_ai_calls(uuid, uuid, text, text, boolean, timestamptz, timestamptz, timestamptz, uuid, int);
RESET ROLE;
DROP INDEX public.ai_calls_tenant_created_idx;
DROP INDEX public.ai_calls_created_idx;
DROP POLICY staff_read_lookup ON public.ai_calls;
-- A table-level REVOKE also removes the column grants.
REVOKE SELECT ON public.ai_calls FROM staff_reader;
