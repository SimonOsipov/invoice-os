-- A public check's AI calls carry no tenant; staff see them as "Readiness check".
-- +goose Up
ALTER TABLE ai_calls ALTER COLUMN tenant_id DROP NOT NULL;
CREATE POLICY public_check_insert ON ai_calls FOR INSERT TO invoice_app
    WITH CHECK (tenant_id IS NULL AND nullif(current_setting('app.current_tenant', true), '') IS NULL);

SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.staff_read_ai_calls(p_actor uuid, p_tenant uuid, p_purpose text, p_outcome text, p_failed boolean,
    p_from timestamptz, p_to timestamptz, p_before_at timestamptz, p_before_id uuid, p_limit int)
RETURNS TABLE (id uuid, tenant_id uuid, tenant_name text, created_at timestamptz, client text, purpose text, model text,
               input_tokens bigint, output_tokens bigint, cost numeric, latency_ms bigint, attempts int, outcome text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    q text := 'SELECT c.id, coalesce(c.tenant_id, ''00000000-0000-0000-0000-000000000000''::uuid), coalesce(t.name, ''Readiness check''), c.created_at, c.client, c.purpose, c.model,
                      c.input_tokens, c.output_tokens, c.cost, c.latency_ms, c.attempts, c.outcome
                 FROM public.ai_calls c LEFT JOIN public.tenants t ON t.id = c.tenant_id
                WHERE true';
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_ai_calls: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- Built per call so each filter set is planned with its own values.
    IF p_tenant = '00000000-0000-0000-0000-000000000000'::uuid THEN q := q || ' AND c.tenant_id IS NULL';
    ELSIF p_tenant IS NOT NULL THEN q := q || ' AND c.tenant_id = $1'; END IF;
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
CREATE OR REPLACE FUNCTION public.staff_read_ai_call_totals(p_actor uuid, p_tenant uuid, p_purpose text, p_outcome text,
    p_from timestamptz, p_to timestamptz, p_before_day date, p_before_name text, p_before_tenant uuid, p_limit int)
RETURNS TABLE (day date, tenant_id uuid, tenant_name text, calls bigint, input_tokens bigint, output_tokens bigint, cost numeric, failed bigint)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    q text := 'SELECT (c.created_at AT TIME ZONE ''UTC'')::date AS day, coalesce(c.tenant_id, ''00000000-0000-0000-0000-000000000000''::uuid) AS tenant_id,
                      coalesce(t.name, ''Readiness check'') AS tenant_name, count(*) AS calls,
                      sum(c.input_tokens)::bigint AS input_tokens, sum(c.output_tokens)::bigint AS output_tokens, sum(c.cost) AS cost,
                      count(*) FILTER (WHERE c.outcome <> ''ok'') AS failed
                 FROM public.ai_calls c LEFT JOIN public.tenants t ON t.id = c.tenant_id
                WHERE true';
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_ai_call_totals: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_tenant = '00000000-0000-0000-0000-000000000000'::uuid THEN q := q || ' AND c.tenant_id IS NULL';
    ELSIF p_tenant IS NOT NULL THEN q := q || ' AND c.tenant_id = $1'; END IF;
    IF p_purpose IS NOT NULL THEN q := q || ' AND c.purpose = $2'; END IF;
    IF p_outcome IS NOT NULL THEN q := q || ' AND c.outcome = $3'; END IF;
    IF p_from IS NOT NULL THEN q := q || ' AND c.created_at >= $4'; END IF;
    IF p_to IS NOT NULL THEN q := q || ' AND c.created_at <= $5'; END IF;
    -- The keyset applies to the groups, so it sits outside the GROUP BY.
    q := 'SELECT g.day, g.tenant_id, g.tenant_name, g.calls, g.input_tokens, g.output_tokens, g.cost, g.failed FROM ('
         || q || ' GROUP BY 1, 2, 3) g WHERE true';
    IF p_before_day IS NOT NULL THEN
        q := q || ' AND (g.day < $6 OR (g.day = $6 AND (g.tenant_name, g.tenant_id) > ($7, $8)))';
    END IF;
    RETURN QUERY EXECUTE q || ' ORDER BY g.day DESC, g.tenant_name, g.tenant_id LIMIT least(coalesce($9, 101), 101)'
        USING p_tenant, p_purpose, p_outcome, p_from, p_to, p_before_day, p_before_name, p_before_tenant, p_limit;
END $$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
-- Keeps NULL-tenant rows and the nullable column: no spend history is lost.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.staff_read_ai_calls(p_actor uuid, p_tenant uuid, p_purpose text, p_outcome text, p_failed boolean,
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
CREATE OR REPLACE FUNCTION public.staff_read_ai_call_totals(p_actor uuid, p_tenant uuid, p_purpose text, p_outcome text,
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
RESET ROLE;
DROP POLICY public_check_insert ON ai_calls;
