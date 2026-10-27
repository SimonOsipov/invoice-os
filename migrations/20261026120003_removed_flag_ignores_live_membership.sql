-- The removed flag answers "no workspace left", so a live membership elsewhere turns it off.

-- +goose Up
-- Owner-created, like the function it replaces: keeps owner and grants.
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.identity_has_removed_membership(p_user_id uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.memberships WHERE user_id = p_user_id AND status = 'removed')
       AND NOT EXISTS (SELECT 1 FROM public.memberships WHERE user_id = p_user_id AND status <> 'removed')
$$;
-- +goose StatementEnd
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.identity_has_removed_membership(p_user_id uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = '' AS $$
    SELECT EXISTS (SELECT 1 FROM public.memberships WHERE user_id = p_user_id AND status = 'removed')
$$;
-- +goose StatementEnd
RESET ROLE;
