-- True when a tenant text read answered from a prompt cut to its size bound. Grant and policy
-- already cover the column.

-- +goose Up
ALTER TABLE extraction_jobs ADD COLUMN ai_text_truncated boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE extraction_jobs DROP COLUMN ai_text_truncated;
