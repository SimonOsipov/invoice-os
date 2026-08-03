-- +goose Up
-- Keyed on tenant_id, NOT (payload->>'id'): jsonb_object_field_text is not LEAKPROOF,
-- so under RLS the payload match can only ever be a heap Filter, never an Index Cond.
-- tenant_id is leakproof uuid_eq (it indexes the RLS qual itself); the trailing id
-- serves ORDER BY id ASC LIMIT 1 so the planner stops preferring audit_log_pkey.
CREATE INDEX audit_log_document_created_idx
    ON audit_log (tenant_id, id) WHERE event = 'document.created';

-- +goose Down
DROP INDEX audit_log_document_created_idx;
