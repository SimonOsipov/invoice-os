-- DEFINER because AUTH-12 keeps invoice_app off `rules`; the function is the only way the app role flips `enabled`.
-- ceiling: any invoice_app code path can call it with a rules-role staff UUID; the route, not the function, writes the audit row.
-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION public.set_rule_enabled(p_actor uuid, p_key text, p_enabled boolean)
RETURNS TABLE (rule_set_version_id uuid, version integer, was_enabled boolean)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v_ver uuid; v_id uuid; v_was boolean;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.staff_members s WHERE s.user_id = p_actor AND s.rules_role) THEN
        RAISE EXCEPTION 'set_rule_enabled: actor is not a rules-role staff member'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    v_ver := public.rule_set_version_for((pg_catalog.now() AT TIME ZONE 'UTC')::date);
    SELECT r.id, r.enabled INTO v_id, v_was FROM public.rules r
     WHERE r.rule_set_version_id = v_ver AND r.key = p_key FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    IF v_was IS DISTINCT FROM p_enabled THEN
        UPDATE public.rules r SET enabled = p_enabled
          FROM public.rule_set_versions v
         WHERE r.rule_set_version_id = v.id
           AND v.id = public.rule_set_version_for((pg_catalog.now() AT TIME ZONE 'UTC')::date)
           AND r.key = p_key;
    END IF;
    RETURN QUERY SELECT v_ver, (SELECT v.version FROM public.rule_set_versions v WHERE v.id = v_ver), v_was;
END $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.set_rule_enabled(uuid, text, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.set_rule_enabled(uuid, text, boolean) TO invoice_app;

-- +goose Down
DROP FUNCTION public.set_rule_enabled(uuid, text, boolean);
