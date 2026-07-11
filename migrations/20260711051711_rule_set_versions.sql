-- +goose Up
-- M3-04: rule_set_versions + rules — GLOBAL rules-as-data reference (roles precedent:
-- no tenant_id, no RLS; every tenant evaluates the same active MBS version). Immutability
-- is at the DB-grant level (audit_log precedent): app gets SELECT on versions, and
-- SELECT + column-level UPDATE(enabled) on rules — so rule CONTENT is frozen for the
-- runtime role while the ONE sanctioned live mutation (the kill-switch) stays open.
-- No rule content is seeded here — M3-05 seeds the first published+active version.
CREATE TABLE rule_set_versions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    version      integer     NOT NULL UNIQUE,
    is_active    boolean     NOT NULL DEFAULT false,
    notes        text,
    published_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX rule_set_versions_one_active ON rule_set_versions ((is_active)) WHERE is_active;

CREATE TABLE rules (
    id                  uuid    PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_set_version_id uuid    NOT NULL REFERENCES rule_set_versions(id) ON DELETE CASCADE,
    key                 text    NOT NULL,
    type                text    NOT NULL CHECK (type IN
                          ('required','format/regex','enum','range','tax_math',
                           'cross_field','conditional','date','cel')),
    target              text    NOT NULL DEFAULT '',
    params              jsonb   NOT NULL DEFAULT '{}',
    severity            text    NOT NULL CHECK (severity IN ('error','warning','info')),
    "when"              text,
    message             text    NOT NULL,
    scope               text    NOT NULL DEFAULT 'document' CHECK (scope IN ('document')),
    enabled             boolean NOT NULL DEFAULT true,
    UNIQUE (rule_set_version_id, key)
);
CREATE INDEX rules_version_idx ON rules (rule_set_version_id);

GRANT SELECT ON rule_set_versions TO invoice_app;
GRANT SELECT, UPDATE (enabled) ON rules TO invoice_app;

-- +goose Down
DROP TABLE rules;
DROP TABLE rule_set_versions;
