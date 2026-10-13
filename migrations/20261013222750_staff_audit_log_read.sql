-- Staff read staff_audit_log only through this definer function; the table has no RLS, so column grants and no policy.
-- +goose Up
GRANT SELECT (id, actor, event, rule_set_version_id, tenant_id, subject_id, payload, created_at) ON public.staff_audit_log TO staff_reader;

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_staff_audit_log(p_actor uuid)
RETURNS TABLE (id bigint, actor uuid, event text, rule_set_version_id uuid, tenant_id uuid, subject_id uuid, payload jsonb, created_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_staff_audit_log: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- ceiling: unpaged; page it when SUPP-07 lists it.
    RETURN QUERY SELECT s.id, s.actor, s.event, s.rule_set_version_id, s.tenant_id, s.subject_id, s.payload, s.created_at
        FROM public.staff_audit_log s ORDER BY s.created_at DESC, s.id DESC;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_staff_audit_log(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_staff_audit_log(uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_staff_audit_log(uuid);
RESET ROLE;
-- A table-level REVOKE also removes the column grants.
REVOKE SELECT ON public.staff_audit_log FROM staff_reader;
