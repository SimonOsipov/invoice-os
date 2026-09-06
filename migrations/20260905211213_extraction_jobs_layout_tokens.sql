-- Page-1 token text for a boxless job: the input LearnBoxlessRule derives a rule from.
-- Nullable with no default -- a PDF job, a pre-migration job and a job whose tokens the Go gate
-- refuses all store NULL. char_length, not octet_length, mirrors layout_anchors' shape.
-- The table-level grant and the FOR ALL tenant_isolation policy already cover a new column.

-- +goose Up
ALTER TABLE extraction_jobs ADD COLUMN layout_tokens jsonb
    CHECK (layout_tokens IS NULL OR
           (jsonb_typeof(layout_tokens) = 'array'
            AND char_length(layout_tokens::text) <= 262144));

-- +goose Down
-- Pure DDL, so no NO FORCE/FORCE toggle: RLS constrains DML, not ALTER TABLE. The predecessor
-- needs one only because its Down runs an UPDATE.
ALTER TABLE extraction_jobs DROP COLUMN layout_tokens;
