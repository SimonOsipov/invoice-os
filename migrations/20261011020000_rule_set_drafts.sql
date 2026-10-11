-- DEFINER because no non-owner role may write rules (TestSchema_OnlyTheOwnerCanWriteRules); these are the app role's only write path.
-- ceiling: any invoice_app code path can call them with a rules-role staff UUID; the route, not the function, writes the audit row.
-- +goose Up
CREATE UNIQUE INDEX rule_set_versions_one_draft ON rule_set_versions ((true)) WHERE NOT sealed;

-- +goose StatementBegin
CREATE FUNCTION public.rule_draft_open(p_actor uuid)
RETURNS TABLE (rule_set_version_id uuid, version integer, from_version integer)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v_src uuid; v_from integer; v_new uuid; v_num integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.staff_members s WHERE s.user_id = p_actor AND s.rules_role) THEN
        RAISE EXCEPTION 'rule_draft_open: actor is not a rules-role staff member'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('rule_set_versions:draft'));
    v_src := public.rule_set_version_for((pg_catalog.now() AT TIME ZONE 'UTC')::date);
    IF v_src IS NULL THEN RETURN; END IF;
    SELECT v.version INTO v_from FROM public.rule_set_versions v WHERE v.id = v_src;
    INSERT INTO public.rule_set_versions (version, sealed, notes)
    VALUES ((SELECT pg_catalog.max(v.version) FROM public.rule_set_versions v) + 1, false, 'Rules desk: from v' || v_from)
    RETURNING id, rule_set_versions.version INTO v_new, v_num;
    INSERT INTO public.rules (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
    SELECT v_new, r.key, r.type, r.target, r.params, r.severity, r."when", r.message, r.scope, r.enabled
      FROM public.rules r WHERE r.rule_set_version_id = v_src;
    RETURN QUERY SELECT v_new, v_num, v_from;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.rule_draft_put_rule(p_actor uuid, p_key text, p_type text, p_target text, p_params jsonb,
                                           p_severity text, p_when text, p_message text, p_enabled boolean, p_create boolean)
RETURNS TABLE (rule_set_version_id uuid, version integer, existed boolean, changed boolean)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v_draft uuid; v_num integer; v_exists boolean;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.staff_members s WHERE s.user_id = p_actor AND s.rules_role) THEN
        RAISE EXCEPTION 'rule_draft_put_rule: actor is not a rules-role staff member'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('rule_set_versions:draft'));
    SELECT v.id, v.version INTO v_draft, v_num FROM public.rule_set_versions v WHERE NOT v.sealed;
    IF v_draft IS NULL THEN RETURN; END IF;
    v_exists := EXISTS (SELECT 1 FROM public.rules r WHERE r.rule_set_version_id = v_draft AND r.key = p_key);
    IF p_create THEN
        IF v_exists THEN
            RETURN QUERY SELECT v_draft, v_num, true, false;
            RETURN;
        END IF;
        INSERT INTO public.rules (rule_set_version_id, key, type, target, params, severity, "when", message, scope, enabled)
        VALUES (v_draft, p_key, p_type, p_target, p_params, p_severity, p_when, p_message, 'document', p_enabled);
        RETURN QUERY SELECT v_draft, v_num, false, true;
        RETURN;
    END IF;
    IF NOT v_exists THEN
        RETURN QUERY SELECT v_draft, v_num, false, false;
        RETURN;
    END IF;
    UPDATE public.rules r
       SET type = p_type, target = p_target, params = p_params, severity = p_severity,
           "when" = p_when, message = p_message, enabled = p_enabled
     WHERE r.rule_set_version_id = v_draft AND r.key = p_key
       AND (r.type, r.target, r.params, r.severity, r."when", r.message, r.enabled)
           IS DISTINCT FROM (p_type, p_target, p_params, p_severity, p_when, p_message, p_enabled);
    RETURN QUERY SELECT v_draft, v_num, true, FOUND;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.rule_draft_remove_rule(p_actor uuid, p_key text)
RETURNS TABLE (rule_set_version_id uuid, version integer, removed boolean)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v_draft uuid; v_num integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.staff_members s WHERE s.user_id = p_actor AND s.rules_role) THEN
        RAISE EXCEPTION 'rule_draft_remove_rule: actor is not a rules-role staff member'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('rule_set_versions:draft'));
    SELECT v.id, v.version INTO v_draft, v_num FROM public.rule_set_versions v WHERE NOT v.sealed;
    IF v_draft IS NULL THEN RETURN; END IF;
    DELETE FROM public.rules r WHERE r.rule_set_version_id = v_draft AND r.key = p_key;
    RETURN QUERY SELECT v_draft, v_num, FOUND;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.rule_draft_publish(p_actor uuid, p_effective_from date)
RETURNS TABLE (rule_set_version_id uuid, version integer, rule_count integer)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE v_draft uuid; v_num integer; v_count integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.staff_members s WHERE s.user_id = p_actor AND s.rules_role) THEN
        RAISE EXCEPTION 'rule_draft_publish: actor is not a rules-role staff member'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('rule_set_versions:draft'));
    SELECT v.id, v.version INTO v_draft, v_num FROM public.rule_set_versions v WHERE NOT v.sealed;
    IF v_draft IS NULL THEN RETURN; END IF;
    IF p_effective_from IS NULL OR p_effective_from < (pg_catalog.now() AT TIME ZONE 'UTC')::date THEN
        RAISE EXCEPTION 'rule_draft_publish: effective date must be today (UTC) or later'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    SELECT pg_catalog.count(*)::integer INTO v_count FROM public.rules r WHERE r.rule_set_version_id = v_draft;
    IF v_count = 0 THEN
        RAISE EXCEPTION 'rule_draft_publish: the draft has no rules'
            USING ERRCODE = 'check_violation';
    END IF;
    UPDATE public.rule_set_versions v
       SET sealed = true, effective_from = p_effective_from, published_at = pg_catalog.now()
     WHERE v.id = v_draft;
    RETURN QUERY SELECT v_draft, v_num, v_count;
END $$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION public.rule_draft_open(uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.rule_draft_put_rule(uuid, text, text, text, jsonb, text, text, text, boolean, boolean) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.rule_draft_remove_rule(uuid, text) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.rule_draft_publish(uuid, date) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.rule_draft_open(uuid) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.rule_draft_put_rule(uuid, text, text, text, jsonb, text, text, text, boolean, boolean) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.rule_draft_remove_rule(uuid, text) TO invoice_app;
GRANT EXECUTE ON FUNCTION public.rule_draft_publish(uuid, date) TO invoice_app;

-- +goose Down
DROP FUNCTION public.rule_draft_publish(uuid, date);
DROP FUNCTION public.rule_draft_remove_rule(uuid, text);
DROP FUNCTION public.rule_draft_put_rule(uuid, text, text, text, jsonb, text, text, text, boolean, boolean);
DROP FUNCTION public.rule_draft_open(uuid);
DROP INDEX rule_set_versions_one_draft;
