-- GoTrue's custom access token hook: projects app_metadata.tenant_id from memberships.
-- SECURITY DEFINER owned by NOLOGIN auth_hook_reader, whose policy is the only cross-tenant
-- read of memberships; supabase_auth_admin gets EXECUTE only.
-- Asserted by custom_access_token_hook_rls_test.go.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION public.custom_access_token_hook(event jsonb) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    -- Exactly one active membership projects a tenant; zero or several strip any incoming one.
    SELECT jsonb_build_object('claims', CASE WHEN count(*) = 1
        THEN jsonb_set(event->'claims', '{app_metadata}',
                 coalesce(event->'claims'->'app_metadata', '{}'::jsonb)
                     || jsonb_build_object('tenant_id', min(m.tenant_id::text)))
        ELSE (event->'claims') #- '{app_metadata,tenant_id}' END)
    FROM public.memberships m
    WHERE m.user_id = (event->>'user_id')::uuid AND m.status = 'active'
$$;
-- +goose StatementEnd

-- Grant and revoke while the migrator still owns the function; after the owner change it cannot.
REVOKE EXECUTE ON FUNCTION public.custom_access_token_hook(jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.custom_access_token_hook(jsonb) TO supabase_auth_admin;

GRANT SELECT (user_id, tenant_id, status) ON public.memberships TO auth_hook_reader;
CREATE POLICY auth_hook_lookup ON public.memberships FOR SELECT TO auth_hook_reader USING (true);

ALTER FUNCTION public.custom_access_token_hook(jsonb) OWNER TO auth_hook_reader;

-- +goose Down
DROP POLICY auth_hook_lookup ON public.memberships;
-- Table-level REVOKE also removes the column grants and still runs if an out-of-order
-- ledger rolled status back first.
REVOKE SELECT ON public.memberships FROM auth_hook_reader;
-- Only the owner can drop it. RESET before goose deletes its version row in this transaction.
SET LOCAL ROLE auth_hook_reader;
DROP FUNCTION public.custom_access_token_hook(jsonb);
RESET ROLE;
