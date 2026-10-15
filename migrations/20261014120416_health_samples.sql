-- One health sample per 5-minute slot, written by the submission sampler, read by staff.
-- +goose Up
CREATE TABLE public.health_samples (
    taken_at      timestamptz PRIMARY KEY,
    fleet_status  text        NOT NULL CHECK (fleet_status IN ('ok', 'degraded', 'unknown')),
    services_down integer     CHECK (services_down >= 0),
    job_counts    jsonb       NOT NULL,
    CONSTRAINT health_samples_down_known CHECK ((fleet_status = 'unknown') = (services_down IS NULL))
);
GRANT INSERT, DELETE ON public.health_samples TO invoice_app;
-- The ON CONFLICT arbiter and the retention DELETE read taken_at.
GRANT SELECT (taken_at) ON public.health_samples TO invoice_app;

-- The sampler has no staff actor: this function returns per-state counts only.
GRANT SELECT (state) ON public.submission_jobs TO auth_hook_reader;
CREATE POLICY health_sample_count ON public.submission_jobs FOR SELECT TO auth_hook_reader USING (true);
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.submission_job_state_counts() RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT coalesce(jsonb_object_agg(c.state, c.n), '{}'::jsonb)
      FROM (SELECT j.state, count(*) AS n FROM public.submission_jobs j GROUP BY j.state) c
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.submission_job_state_counts() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.submission_job_state_counts() TO invoice_app;
RESET ROLE;

GRANT SELECT (taken_at, fleet_status, services_down, job_counts) ON public.health_samples TO staff_reader;

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_submission_job_counts(p_actor uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_submission_job_counts: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN (SELECT coalesce(jsonb_object_agg(c.state, c.n), '{}'::jsonb)
              FROM (SELECT j.state, count(*) AS n FROM public.submission_jobs j GROUP BY j.state) c);
END $$;
-- +goose StatementEnd
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
REVOKE EXECUTE ON FUNCTION public.staff_read_submission_job_counts(uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.staff_read_health_samples(uuid, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_submission_job_counts(uuid) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.staff_read_health_samples(uuid, timestamptz) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_health_samples(uuid, timestamptz);
DROP FUNCTION public.staff_read_submission_job_counts(uuid);
RESET ROLE;
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.submission_job_state_counts();
RESET ROLE;
DROP POLICY health_sample_count ON public.submission_jobs;
REVOKE SELECT ON public.submission_jobs FROM auth_hook_reader;
DROP TABLE public.health_samples;
