-- extraction_anchor_rules — one row per learned anchor rule, per tenant, per layout.
--
-- The only FK is tenant_id -> tenants. The house composite (tenant_id, col) FK rule exists
-- because referential checks run RLS-bypassed, so a bare FK to a TENANT-SCOPED parent would
-- accept another tenant's row. A rule belongs to a computed layout fingerprint, not to a row,
-- so there is no such parent here and the rule is satisfied vacuously. _tenant_id_id_uq is
-- still a CONSTRAINT and not a bare unique index, so EXTR-14's child has a composite-FK
-- target.
--
-- No UNIQUE over (tenant_id, layout_fingerprint, field_name): corrections are append-only, so
-- several rules accumulate per field per layout and all produce candidates.
--
-- SELECT only. EXTR-14 adds INSERT in the migration that ships its writer.

-- +goose Up
CREATE TABLE extraction_anchor_rules (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- "v1:<64 hex>", 67 bytes today. The ceiling is 128, not 67: bumping FingerprintVersion
    -- invalidates every row with no migration, and an exact-length CHECK would turn that
    -- lever into one. Go owns the exact shape (fingerprint_test.go F-05/F-10).
    layout_fingerprint  text        NOT NULL CHECK (char_length(layout_fingerprint) > 0
                                               AND char_length(layout_fingerprint) <= 128),
    -- An invoices column name; extraction.HeaderFields is the vocabulary. Same 128 ceiling
    -- as extraction_field_results.field_name.
    field_name          text        NOT NULL CHECK (char_length(field_name) > 0
                                               AND char_length(field_name) <= 128),
    -- The floor, not the validator: SQL cannot compile RE2, so ParseRule owns the enums, the
    -- label cap and compilation. The 'label' key requirement is the one place this CHECK is
    -- STRONGER than ParseRule, which accepts a missing label as the empty pattern.
    rule                jsonb       NOT NULL CHECK (jsonb_typeof(rule) = 'object'
                                               AND rule ? 'label'
                                               AND rule ? 'relation'
                                               AND rule ? 'shape'),
    rule_schema_version int         NOT NULL CHECK (rule_schema_version >= 1),
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT extraction_anchor_rules_tenant_id_id_uq UNIQUE (tenant_id, id)
);

-- The only read this table serves: "what rules apply to a document that looks like this one".
CREATE INDEX extraction_anchor_rules_tenant_fingerprint_idx
    ON extraction_anchor_rules (tenant_id, layout_fingerprint);

-- Force is what subjects the table owner (the migrator) to the policy below; enable alone
-- would let it bypass (docs/migrations.md section 1).
ALTER TABLE extraction_anchor_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE extraction_anchor_rules FORCE  ROW LEVEL SECURITY;

-- No TO clause, so the policy binds every role. An unset GUC yields NULL and therefore no
-- rows.
CREATE POLICY tenant_isolation ON extraction_anchor_rules
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Least privilege (docs/migrations.md section 3). Nothing writes here until EXTR-14, and
-- nothing to invoice_tenant_reader: it enumerates tenants and has no use for learned rules.
GRANT SELECT ON extraction_anchor_rules TO invoice_app;

-- +goose Down
DROP TABLE extraction_anchor_rules;
