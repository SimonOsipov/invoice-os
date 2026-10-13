-- Staff read tenant rows only through staff_read_* definer functions owned by NOLOGIN staff_reader.
-- ceiling: any invoice_app path can pass a house member's UUID; TestRLS_StaffReadFunctionsAreCalledOnlyInsideWithinStaffTx pins the callers.
-- +goose Up
GRANT SELECT (id, name, kind, created_at) ON public.tenants TO staff_reader;
CREATE POLICY staff_read_lookup ON public.tenants FOR SELECT TO staff_reader USING (true);

-- Only the owner grants EXECUTE on is_house_member.
SET LOCAL ROLE auth_hook_reader;
GRANT EXECUTE ON FUNCTION public.is_house_member(uuid) TO staff_reader;
RESET ROLE;

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_tenants(p_actor uuid)
RETURNS TABLE (id uuid, name text, kind text, created_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_tenants: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN QUERY SELECT t.id, t.name, t.kind, t.created_at FROM public.tenants t ORDER BY t.created_at DESC, t.id;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_tenants(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_tenants(uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_tenants(uuid);
RESET ROLE;
SET LOCAL ROLE auth_hook_reader;
REVOKE EXECUTE ON FUNCTION public.is_house_member(uuid) FROM staff_reader;
RESET ROLE;
DROP POLICY staff_read_lookup ON public.tenants;
-- A table-level REVOKE also removes the column grants.
REVOKE SELECT ON public.tenants FROM staff_reader;
