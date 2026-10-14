-- Tenant activity, members and recent invoices for staff, through staff_read_ definer functions.
-- +goose Up
GRANT SELECT (tenant_id, user_id, role, status, display_name, email, created_at) ON public.memberships TO staff_reader;
CREATE POLICY staff_read_lookup ON public.memberships FOR SELECT TO staff_reader USING (true);
GRANT SELECT (id, tenant_id, invoice_number, status, created_at) ON public.invoices TO staff_reader;
CREATE POLICY staff_read_lookup ON public.invoices FOR SELECT TO staff_reader USING (true);
GRANT SELECT (tenant_id, created_at) ON public.audit_log TO staff_reader;
CREATE POLICY staff_read_lookup ON public.audit_log FOR SELECT TO staff_reader USING (true);

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_tenant_activity(p_actor uuid)
RETURNS TABLE (tenant_id uuid, active_members bigint, invoice_count bigint, last_activity_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_tenant_activity: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- ceiling: three subqueries per tenant; aggregate in one pass above ~1k tenants
    RETURN QUERY SELECT t.id,
        (SELECT count(*) FROM public.memberships m WHERE m.tenant_id = t.id AND m.status = 'active'),
        (SELECT count(*) FROM public.invoices i WHERE i.tenant_id = t.id),
        (SELECT max(a.created_at) FROM public.audit_log a WHERE a.tenant_id = t.id)
    FROM public.tenants t;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_tenant_members(p_actor uuid, p_tenant uuid)
RETURNS TABLE (user_id uuid, role text, status text, display_name text, email text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_tenant_members: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT m.user_id, m.role, m.status, m.display_name, m.email FROM public.memberships m
        WHERE m.tenant_id = p_tenant ORDER BY m.created_at, m.user_id;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_tenant_invoices(p_actor uuid, p_tenant uuid)
RETURNS TABLE (id uuid, invoice_number text, status text, created_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_tenant_invoices: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT i.id, i.invoice_number, i.status, i.created_at FROM public.invoices i
        WHERE i.tenant_id = p_tenant ORDER BY i.created_at DESC, i.id LIMIT 10;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_tenant_activity(uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.staff_read_tenant_members(uuid, uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.staff_read_tenant_invoices(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_tenant_activity(uuid) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.staff_read_tenant_members(uuid, uuid) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.staff_read_tenant_invoices(uuid, uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_tenant_invoices(uuid, uuid);
DROP FUNCTION public.staff_read_tenant_members(uuid, uuid);
DROP FUNCTION public.staff_read_tenant_activity(uuid);
RESET ROLE;
DROP POLICY staff_read_lookup ON public.audit_log;
DROP POLICY staff_read_lookup ON public.invoices;
DROP POLICY staff_read_lookup ON public.memberships;
-- A table-level REVOKE also removes the column grants.
REVOKE SELECT ON public.audit_log FROM staff_reader;
REVOKE SELECT ON public.invoices FROM staff_reader;
REVOKE SELECT ON public.memberships FROM staff_reader;
