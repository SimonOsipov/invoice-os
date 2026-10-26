-- A tenant-less staff search by event or actor reads these instead of scanning audit_log.
-- +goose Up
CREATE INDEX audit_log_event_created_idx ON public.audit_log (event, created_at DESC, id DESC);
CREATE INDEX audit_log_actor_created_idx ON public.audit_log (actor, created_at DESC, id DESC);

-- +goose Down
DROP INDEX public.audit_log_actor_created_idx;
DROP INDEX public.audit_log_event_created_idx;
