-- Staff search audit_log across tenants through a staff_read_ definer function; created_at leads the one index a tenant-less search needs.
-- +goose Up
GRANT SELECT (id, event, actor, payload) ON public.audit_log TO staff_reader;
CREATE INDEX audit_log_created_idx ON public.audit_log (created_at DESC, id DESC);

-- Created as the owner, so the owner's REVOKE and GRANT stand.
SET LOCAL ROLE staff_reader;
-- +goose StatementBegin
CREATE FUNCTION public.staff_read_audit_log(p_actor uuid, p_tenant uuid, p_event text, p_subject text,
    p_from timestamptz, p_to timestamptz, p_before_at timestamptz, p_before_id bigint, p_limit int)
RETURNS TABLE (id bigint, tenant_id uuid, tenant_name text, created_at timestamptz, event text, actor text,
               member_name text, member_email text, payload jsonb)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    q text := 'SELECT a.id, a.tenant_id, t.name, a.created_at, a.event, a.actor, m.display_name, m.email, a.payload
                 FROM public.audit_log a
                 LEFT JOIN public.tenants t ON t.id = a.tenant_id
                 LEFT JOIN public.memberships m ON m.tenant_id = a.tenant_id AND m.user_id::text = a.actor
                WHERE true';
BEGIN
    IF NOT public.is_house_member(p_actor) THEN
        RAISE EXCEPTION 'staff_read_audit_log: actor is not staff' USING ERRCODE = 'insufficient_privilege';
    END IF;
    -- Built per call so each filter set is planned with its own values; a NULL guard would hide the index.
    IF p_tenant IS NOT NULL THEN q := q || ' AND a.tenant_id = $1'; END IF;
    IF p_event IS NOT NULL THEN q := q || ' AND a.event = $2'; END IF;
    IF p_subject IS NOT NULL THEN q := q || ' AND a.actor = $3'; END IF;
    IF p_from IS NOT NULL THEN q := q || ' AND a.created_at >= $4'; END IF;
    IF p_to IS NOT NULL THEN q := q || ' AND a.created_at <= $5'; END IF;
    IF p_before_at IS NOT NULL THEN q := q || ' AND (a.created_at, a.id) < ($6, $7)'; END IF;
    RETURN QUERY EXECUTE q || ' ORDER BY a.created_at DESC, a.id DESC LIMIT least(coalesce($8, 101), 101)'
        USING p_tenant, p_event, p_subject, p_from, p_to, p_before_at, p_before_id, p_limit;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.staff_read_audit_log(uuid, uuid, text, text, timestamptz, timestamptz, timestamptz, bigint, int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.staff_read_audit_log(uuid, uuid, text, text, timestamptz, timestamptz, timestamptz, bigint, int) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE staff_reader;
DROP FUNCTION public.staff_read_audit_log(uuid, uuid, text, text, timestamptz, timestamptz, timestamptz, bigint, int);
RESET ROLE;
DROP INDEX public.audit_log_created_idx;
REVOKE SELECT (id, event, actor, payload) ON public.audit_log FROM staff_reader;
