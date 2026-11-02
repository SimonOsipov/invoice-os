-- +goose Up
-- ceiling: invoice_app learns whether a user id holds a live membership in any workspace; revisit if invoice_app stops being trusted code.
SET LOCAL ROLE auth_hook_reader;
GRANT EXECUTE ON FUNCTION public.identity_has_membership(uuid) TO invoice_app;
RESET ROLE;

-- +goose Down
SET LOCAL ROLE auth_hook_reader;
REVOKE EXECUTE ON FUNCTION public.identity_has_membership(uuid) FROM invoice_app;
RESET ROLE;
