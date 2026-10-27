-- ceiling: any invoice_app service can erase the invitations of an address that has an unfinished person_erasures row, across tenants (wider than purge_tenant); revisit if invoice_app stops being trusted code.
-- ceiling: invitations_to_erase seq-scans invitations; index lower(invitee_email) above ~10k rows.
-- +goose Up
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.invitations_to_erase(p_user_id uuid, p_email text, p_unheld boolean)
RETURNS TABLE (tenant_id uuid, invitation_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT i.tenant_id, i.id
      FROM public.invitations i
     WHERE pg_catalog.lower(pg_catalog.btrim(i.invitee_email)) = pg_catalog.lower(pg_catalog.btrim(p_email))
       AND ((i.status = 'accepted'
             AND EXISTS (SELECT 1 FROM public.memberships m
                          WHERE m.tenant_id = i.tenant_id AND m.user_id = p_user_id))
            OR (p_unheld AND i.status IN ('pending', 'revoked')))
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.invitations_to_erase(uuid, text, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.invitations_to_erase(uuid, text, boolean) TO invoice_migrator;
RESET ROLE;

-- +goose StatementBegin
CREATE FUNCTION public.erase_invitation_emails(p_user_id uuid, p_email text, p_unheld boolean) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = '' AS $$
DECLARE
    v_email text := pg_catalog.lower(pg_catalog.btrim(p_email));
    v_saved text;
    v_tenant uuid;
    v_ids uuid[];
    v_n integer;
    v_total integer := 0;
BEGIN
    IF p_user_id IS NULL OR v_email IS NULL OR v_email = '' THEN
        RETURN 0;
    END IF;
    -- Only an address under a recorded, running erasure may be erased.
    IF NOT EXISTS (SELECT 1 FROM public.person_erasures
                    WHERE user_id = p_user_id AND email = v_email AND finished_at IS NULL) THEN
        RETURN 0;
    END IF;

    v_saved := pg_catalog.current_setting('app.current_tenant', true);
    FOR v_tenant, v_ids IN
        SELECT f.tenant_id, pg_catalog.array_agg(f.invitation_id)
          FROM public.invitations_to_erase(p_user_id, v_email, p_unheld) f
         GROUP BY f.tenant_id
    LOOP
        PERFORM pg_catalog.set_config('app.current_tenant', v_tenant::text, true);
        DELETE FROM public.invitations WHERE tenant_id = v_tenant AND id = ANY (v_ids);
        GET DIAGNOSTICS v_n = ROW_COUNT;
        v_total := v_total + v_n;
    END LOOP;
    PERFORM pg_catalog.set_config('app.current_tenant', coalesce(v_saved, ''), true);
    RETURN v_total;
END
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.erase_invitation_emails(uuid, text, boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.erase_invitation_emails(uuid, text, boolean) TO invoice_app;

-- +goose Down
DROP FUNCTION public.erase_invitation_emails(uuid, text, boolean);
-- Only the owner can drop it. RESET before goose deletes its version row in this transaction.
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.invitations_to_erase(uuid, text, boolean);
RESET ROLE;
