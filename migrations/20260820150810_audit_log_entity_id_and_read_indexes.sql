-- +goose Up
-- No FK on entity_id: this table's tenant_id has none for the same reason, and under
-- either referential action a delete of the referenced row must UPDATE or DELETE an
-- append-only table, which its own trigger refuses.
--
-- tenant_id leads every index so the RLS qual is a leakproof uuid_eq the planner can
-- turn into an Index Cond; the trailing id DESC makes the keyset page a pure index read.
--
-- The whole file must stay inside goose's transaction, which also rules out CONCURRENTLY.
-- Its backfill half suspends the append-only trigger, and only crash-rollback of a single
-- transaction guarantees a process death never leaves that trigger disabled.
ALTER TABLE audit_log ADD COLUMN entity_id uuid;

CREATE INDEX audit_log_tenant_created_idx
    ON audit_log (tenant_id, created_at DESC, id DESC);

CREATE INDEX audit_log_tenant_event_created_idx
    ON audit_log (tenant_id, event, created_at DESC, id DESC);

CREATE INDEX audit_log_tenant_actor_created_idx
    ON audit_log (tenant_id, actor, created_at DESC, id DESC);

CREATE INDEX audit_log_tenant_entity_created_idx
    ON audit_log (tenant_id, entity_id, created_at DESC, id DESC);

-- +goose Down
-- DROP COLUMN auto-drops only the entity index; the other three must be named. Keep the
-- entity DROP INDEX ahead of the column drop -- after it, the index is already gone.
DROP INDEX audit_log_tenant_entity_created_idx;
DROP INDEX audit_log_tenant_actor_created_idx;
DROP INDEX audit_log_tenant_event_created_idx;
DROP INDEX audit_log_tenant_created_idx;
ALTER TABLE audit_log DROP COLUMN entity_id;
