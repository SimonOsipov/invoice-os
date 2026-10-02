-- Staff role: a row here makes the access-token hook add app_metadata.staff = true.
-- Written only by the owner or a superuser (docs/identity-provider.md "Granting staff").
-- ceiling: a staff user whose app_metadata is not an object gets {"staff": true} only, dropping a projected tenant; GoTrue always sends an object.

-- +goose Up
CREATE TABLE public.staff_members (
    user_id    uuid PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE public.staff_members ENABLE ROW LEVEL SECURITY;
GRANT SELECT (user_id) ON public.staff_members TO auth_hook_reader;
CREATE POLICY staff_hook_lookup ON public.staff_members FOR SELECT TO auth_hook_reader USING (true);

-- Only the owner can replace the hook; CREATE OR REPLACE keeps its EXECUTE grants.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.custom_access_token_hook(event jsonb) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    -- Tenant step unchanged; then staff set from staff_members, else any incoming staff stripped.
    WITH t AS (
        SELECT CASE WHEN count(*) = 1
            THEN jsonb_set(event->'claims', '{app_metadata}',
                     coalesce(event->'claims'->'app_metadata', '{}'::jsonb)
                         || jsonb_build_object('tenant_id', min(m.tenant_id::text)))
            ELSE (event->'claims') #- '{app_metadata,tenant_id}' END AS claims
        FROM public.memberships m
        WHERE m.user_id = (event->>'user_id')::uuid AND m.status = 'active'
    )
    SELECT jsonb_build_object('claims', CASE
        WHEN EXISTS (SELECT 1 FROM public.staff_members s WHERE s.user_id = (event->>'user_id')::uuid)
            THEN jsonb_set(t.claims, '{app_metadata}',
                     CASE WHEN jsonb_typeof(t.claims->'app_metadata') = 'object'
                          THEN t.claims->'app_metadata' ELSE '{}'::jsonb END
                         || '{"staff": true}'::jsonb)
        -- GoTrue always sends an object.
        WHEN jsonb_typeof(t.claims->'app_metadata') = 'object'
            THEN t.claims #- '{app_metadata,staff}'
        ELSE t.claims END)
    FROM t
$$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.custom_access_token_hook(event jsonb) RETURNS jsonb
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
RESET ROLE;
DROP POLICY staff_hook_lookup ON public.staff_members;
REVOKE SELECT ON public.staff_members FROM auth_hook_reader;
DROP TABLE public.staff_members;
