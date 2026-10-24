-- Per-kind River counts per sample; NULL marks a sample from before per-kind sampling.
-- +goose Up
ALTER TABLE public.health_samples ADD COLUMN kind_counts jsonb;
GRANT SELECT (kind_counts) ON public.health_samples TO staff_reader;
GRANT SELECT (kind, state) ON public.river_job TO staff_reader;

SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_health_samples(uuid, timestamptz);
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_health_samples(p_actor uuid, p_since timestamptz)
RETURNS TABLE (taken_at timestamptz, fleet_status text, services_down integer, job_counts jsonb, kind_counts jsonb)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_health_samples: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT s.taken_at, s.fleet_status, s.services_down, s.job_counts, s.kind_counts FROM public.health_samples s
        WHERE s.taken_at >= p_since ORDER BY s.taken_at;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_river_job_counts(p_actor uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_river_job_counts: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN (SELECT coalesce(jsonb_object_agg(k.kind, k.states), '{}'::jsonb)
              FROM (SELECT c.kind, jsonb_object_agg(c.state, c.n) AS states
                      FROM (SELECT j.kind, j.state::text AS state, count(*) AS n FROM public.river_job j GROUP BY j.kind, j.state) c
                     GROUP BY c.kind) k);
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_health_samples(uuid, timestamptz) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.staff_read_river_job_counts(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_health_samples(uuid, timestamptz) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.staff_read_river_job_counts(uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_river_job_counts(uuid);
DROP FUNCTION public.staff_read_health_samples(uuid, timestamptz);
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_health_samples(p_actor uuid, p_since timestamptz)
RETURNS TABLE (taken_at timestamptz, fleet_status text, services_down integer, job_counts jsonb)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_health_samples: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT s.taken_at, s.fleet_status, s.services_down, s.job_counts FROM public.health_samples s
        WHERE s.taken_at >= p_since ORDER BY s.taken_at;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_health_samples(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_health_samples(uuid, timestamptz) TO invoice_app;
RESET ROLE;
REVOKE SELECT (kind, state) ON public.river_job FROM staff_reader;
REVOKE SELECT (kind_counts) ON public.health_samples FROM staff_reader;
ALTER TABLE public.health_samples DROP COLUMN kind_counts;
