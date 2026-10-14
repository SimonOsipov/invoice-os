-- Submission jobs and invoice status history for staff, through staff_read_ definer functions.
-- +goose Up
GRANT SELECT (id, tenant_id, invoice_id, state, attempts, last_error, poll_ref, created_at, updated_at) ON public.submission_jobs TO staff_reader;
CREATE POLICY staff_read_lookup ON public.submission_jobs FOR SELECT TO staff_reader USING (true);
GRANT SELECT (id, tenant_id, invoice_id, from_status, to_status, actor, changed_at) ON public.invoice_status_history TO staff_reader;
CREATE POLICY staff_read_lookup ON public.invoice_status_history FOR SELECT TO staff_reader USING (true);

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_submission_jobs(p_actor uuid, p_state text, p_tenant uuid)
RETURNS TABLE (id uuid, tenant_id uuid, tenant_name text, invoice_id uuid, invoice_number text, invoice_status text,
               state text, attempts int, last_error text, acknowledged boolean, created_at timestamptz, updated_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_submission_jobs: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- ceiling: newest 200 per filter; page it when a filter holds more
    RETURN QUERY SELECT j.id, j.tenant_id, t.name, j.invoice_id, i.invoice_number, i.status,
        j.state, j.attempts, j.last_error, j.poll_ref IS NOT NULL, j.created_at, j.updated_at
    FROM public.submission_jobs j
    JOIN public.tenants t ON t.id = j.tenant_id
    JOIN public.invoices i ON i.id = j.invoice_id
    WHERE (p_state IS NULL OR j.state = p_state) AND (p_tenant IS NULL OR j.tenant_id = p_tenant)
    ORDER BY j.created_at DESC, j.id LIMIT 200;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_submission_job(p_actor uuid, p_id uuid)
RETURNS TABLE (id uuid, tenant_id uuid, tenant_name text, invoice_id uuid, invoice_number text, invoice_status text,
               state text, attempts int, last_error text, acknowledged boolean, created_at timestamptz, updated_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_submission_job: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT j.id, j.tenant_id, t.name, j.invoice_id, i.invoice_number, i.status,
        j.state, j.attempts, j.last_error, j.poll_ref IS NOT NULL, j.created_at, j.updated_at
    FROM public.submission_jobs j
    JOIN public.tenants t ON t.id = j.tenant_id
    JOIN public.invoices i ON i.id = j.invoice_id
    WHERE j.id = p_id;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_invoice_status_history(p_actor uuid, p_tenant uuid, p_invoice uuid)
RETURNS TABLE (from_status text, to_status text, actor text, changed_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_invoice_status_history: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT h.from_status, h.to_status, h.actor, h.changed_at
    FROM public.invoice_status_history h WHERE h.tenant_id = p_tenant AND h.invoice_id = p_invoice
    ORDER BY h.changed_at, h.id;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_submission_jobs(uuid, text, uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.staff_read_submission_job(uuid, uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.staff_read_invoice_status_history(uuid, uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_submission_jobs(uuid, text, uuid) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.staff_read_submission_job(uuid, uuid) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.staff_read_invoice_status_history(uuid, uuid, uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_invoice_status_history(uuid, uuid, uuid);
DROP FUNCTION public.staff_read_submission_job(uuid, uuid);
DROP FUNCTION public.staff_read_submission_jobs(uuid, text, uuid);
RESET ROLE;
DROP POLICY staff_read_lookup ON public.invoice_status_history;
DROP POLICY staff_read_lookup ON public.submission_jobs;
-- A table-level REVOKE also removes the column grants.
REVOKE SELECT ON public.invoice_status_history FROM staff_reader;
REVOKE SELECT ON public.submission_jobs FROM staff_reader;
