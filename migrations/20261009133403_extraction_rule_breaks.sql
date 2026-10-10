-- extraction_rule_breaks: append-only record of rule violations found on a read. Composite
-- FK to extraction_jobs because RI checks bypass RLS. The reason_code CHECK is widened to
-- rule_break with no writer in extraction_field_results; it tracks the Go enum.

-- +goose Up
ALTER TABLE extraction_field_results DROP CONSTRAINT extraction_field_results_reason_code_check;
ALTER TABLE extraction_field_results ADD CONSTRAINT extraction_field_results_reason_code_check
    CHECK (reason_code IS NULL OR reason_code IN
        ('unreadable', 'ambiguous', 'inconsistent', 'missing', 'rule_break'));

CREATE TABLE extraction_rule_breaks (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    extraction_job_id   uuid        NOT NULL,
    field_name          text        NOT NULL CHECK (char_length(field_name) > 0
                                                AND char_length(field_name) <= 128),
    rule_set_version_id uuid        NOT NULL REFERENCES rule_set_versions(id),
    rule_key            text        NOT NULL CHECK (char_length(rule_key) > 0),
    message             text        NOT NULL CHECK (char_length(message) > 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT extraction_rule_breaks_tenant_job_fk
        FOREIGN KEY (tenant_id, extraction_job_id)
        REFERENCES extraction_jobs (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT extraction_rule_breaks_one_per_rule
        UNIQUE (tenant_id, extraction_job_id, field_name, rule_key)
);

ALTER TABLE extraction_rule_breaks ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_rule_breaks FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON extraction_rule_breaks
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

GRANT SELECT, INSERT ON extraction_rule_breaks TO invoice_app;

-- +goose Down
DROP TABLE extraction_rule_breaks;

ALTER TABLE extraction_field_results DROP CONSTRAINT extraction_field_results_reason_code_check;
ALTER TABLE extraction_field_results ADD CONSTRAINT extraction_field_results_reason_code_check
    CHECK (reason_code IS NULL OR reason_code IN
        ('unreadable', 'ambiguous', 'inconsistent', 'missing'));
