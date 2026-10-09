-- Rules role: a staff row with rules_role true makes the access-token hook add app_metadata.rules_role = true.
-- Written only by the owner or a superuser, like staff_members.
-- ceiling: a staff user whose app_metadata is not an object gets only the staff keys, dropping a projected tenant; GoTrue always sends an object.

-- +goose Up
ALTER TABLE public.staff_members ADD COLUMN rules_role boolean NOT NULL DEFAULT false;
GRANT SELECT (rules_role) ON public.staff_members TO auth_hook_reader;

-- Only the owner can replace the hook; CREATE OR REPLACE keeps its EXECUTE grants.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.custom_access_token_hook(event jsonb) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    -- Tenant step unchanged; then staff and rules_role set from staff_members, else both incoming keys stripped.
    WITH t AS (
        SELECT CASE WHEN count(*) = 1
            THEN jsonb_set(event->'claims', '{app_metadata}',
                     coalesce(event->'claims'->'app_metadata', '{}'::jsonb)
                         || jsonb_build_object('tenant_id', min(m.tenant_id::text)))
            ELSE (event->'claims') #- '{app_metadata,tenant_id}' END AS claims
        FROM public.memberships m
        WHERE m.user_id = (event->>'user_id')::uuid AND m.status = 'active'
    ), st AS (
        SELECT s.rules_role FROM public.staff_members s WHERE s.user_id = (event->>'user_id')::uuid
    )
    SELECT jsonb_build_object('claims', CASE
        WHEN EXISTS (SELECT 1 FROM st)
            THEN jsonb_set(t.claims, '{app_metadata}',
                     (CASE WHEN jsonb_typeof(t.claims->'app_metadata') = 'object'
                           THEN t.claims->'app_metadata' ELSE '{}'::jsonb END #- '{rules_role}')
                         || '{"staff": true}'::jsonb
                         || CASE WHEN (SELECT rules_role FROM st)
                                 THEN '{"rules_role": true}'::jsonb ELSE '{}'::jsonb END)
        -- GoTrue always sends an object.
        WHEN jsonb_typeof(t.claims->'app_metadata') = 'object'
            THEN t.claims #- '{app_metadata,staff}' #- '{app_metadata,rules_role}'
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
ALTER TABLE public.staff_members DROP COLUMN rules_role;
