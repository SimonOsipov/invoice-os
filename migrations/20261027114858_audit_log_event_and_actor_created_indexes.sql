-- A tenant-less staff search by event or actor reads these instead of scanning audit_log.
-- +goose Up
-- CREATE INDEX holds a SHARE lock, blocking audit writes, so the build stays under a row ceiling.
-- FORCE RLS hides every row from the migrator; lift it to count, then restore it.
-- +goose StatementBegin
DO $guard$
DECLARE
    v_rows bigint;
BEGIN
    ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY;
    SELECT count(*) INTO v_rows FROM audit_log;
    IF v_rows > 500000 THEN
        RAISE EXCEPTION 'audit_log has % rows, above the 500000 in-migration ceiling; build audit_log_event_created_idx and audit_log_actor_created_idx out of band, then redeploy', v_rows
            USING ERRCODE = 'program_limit_exceeded';
    END IF;
    ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
END
$guard$;
-- +goose StatementEnd

CREATE INDEX audit_log_event_created_idx ON public.audit_log (event, created_at DESC, id DESC);
CREATE INDEX audit_log_actor_created_idx ON public.audit_log (actor, created_at DESC, id DESC);

-- +goose Down
DROP INDEX public.audit_log_actor_created_idx;
DROP INDEX public.audit_log_event_created_idx;
