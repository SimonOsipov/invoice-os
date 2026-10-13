-- +goose Up
-- Global, no tenant, no RLS: invoice_app may only INSERT, so no app connection reads it.
-- The owner invoice_migrator reads it and is held append-only by the trigger below.
CREATE TABLE public.staff_audit_log (
    id                  bigserial   PRIMARY KEY,
    actor               uuid        NOT NULL,
    event               text        NOT NULL,
    rule_set_version_id uuid        NOT NULL,
    payload             jsonb       NOT NULL DEFAULT '{}',
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT staff_audit_actor_set    CHECK (actor <> '00000000-0000-0000-0000-000000000000'),
    CONSTRAINT staff_audit_version_set  CHECK (rule_set_version_id <> '00000000-0000-0000-0000-000000000000'),
    CONSTRAINT staff_audit_event_length CHECK (char_length(event) > 0 AND char_length(event) < 128)
);

GRANT INSERT ON public.staff_audit_log TO invoice_app;
GRANT USAGE ON SEQUENCE public.staff_audit_log_id_seq TO invoice_app;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.staff_audit_log_append_only()
    RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'staff_audit_log is append-only: % is not permitted', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER staff_audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON public.staff_audit_log
    FOR EACH ROW EXECUTE FUNCTION public.staff_audit_log_append_only();

CREATE TRIGGER staff_audit_log_no_truncate
    BEFORE TRUNCATE ON public.staff_audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION public.staff_audit_log_append_only();

-- +goose Down
DROP TABLE public.staff_audit_log;
DROP FUNCTION public.staff_audit_log_append_only();
