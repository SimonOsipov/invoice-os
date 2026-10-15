-- Active members of the one tenant named here are staff. No row is inserted: production's is written by hand.
-- +goose Up
CREATE TABLE public.house_company (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    tenant_id uuid    NOT NULL REFERENCES public.tenants (id)
);
ALTER TABLE public.house_company ENABLE ROW LEVEL SECURITY;
GRANT SELECT (tenant_id) ON public.house_company TO auth_hook_reader;
CREATE POLICY house_hook_lookup ON public.house_company FOR SELECT TO auth_hook_reader USING (true);

-- Created as the owner: an ALTER OWNER after the GRANT would drop the migrator's grant.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE FUNCTION public.is_house_member(p_user_id uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.memberships m JOIN public.house_company h ON h.tenant_id = m.tenant_id
                    WHERE m.user_id = p_user_id AND m.status = 'active')
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.is_house_member(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.is_house_member(uuid) TO invoice_migrator;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.custom_access_token_hook(event jsonb) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    -- Tenant step unchanged; staff from house membership, else staff stripped; rules_role always stripped.
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
        WHEN public.is_house_member((event->>'user_id')::uuid)
            THEN jsonb_set(t.claims, '{app_metadata}',
                     (CASE WHEN jsonb_typeof(t.claims->'app_metadata') = 'object'
                           THEN t.claims->'app_metadata' ELSE '{}'::jsonb END #- '{rules_role}')
                         || '{"staff": true}'::jsonb)
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
DROP FUNCTION public.is_house_member(uuid);
RESET ROLE;
DROP TABLE public.house_company;
