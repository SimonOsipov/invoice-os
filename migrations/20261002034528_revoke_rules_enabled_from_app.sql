-- Golden rules are staff-managed.

-- +goose Up
REVOKE UPDATE (enabled) ON rules FROM invoice_app;

-- A REVOKE by a non-grantor only warns; fail the deploy instead.
-- +goose StatementBegin
DO $$
BEGIN
    IF has_column_privilege('invoice_app', 'public.rules', 'enabled', 'UPDATE') THEN
        RAISE EXCEPTION 'invoice_app still holds UPDATE on rules.enabled';
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
GRANT UPDATE (enabled) ON rules TO invoice_app;
