-- What a learned rule is keyed to, and the grant that writes one.
--
-- seq is the recency order, not created_at: created_at defaults to now(), which is
-- transaction-constant, so two rules written together tie on it exactly while nextval
-- separates them. No new policy — tenant_isolation is FOR ALL with USING only, and
-- Postgres reuses USING as the WITH CHECK when one is omitted.

-- +goose Up
-- Nullable, no default: a job written before this migration has no fingerprint, and a
-- correction on one of those learns nothing. Retro-application is out of scope.
ALTER TABLE extraction_jobs ADD COLUMN layout_fingerprint text
    CHECK (layout_fingerprint IS NULL OR
           (char_length(layout_fingerprint) > 0 AND char_length(layout_fingerprint) <= 128));

-- An array of anchor observations, in AnchorObservations order. Go owns the element
-- shape; SQL owns only the top-level type.
ALTER TABLE extraction_jobs ADD COLUMN layout_anchors jsonb
    CHECK (layout_anchors IS NULL OR jsonb_typeof(layout_anchors) = 'array');

ALTER TABLE extraction_anchor_rules ADD COLUMN seq bigserial NOT NULL;

-- Supersedes extraction_anchor_rules_tenant_fingerprint_idx (20260829082535): same leading
-- prefix, plus the recency order the resolver reads by.
CREATE INDEX extraction_anchor_rules_tenant_fingerprint_seq_idx
    ON extraction_anchor_rules (tenant_id, layout_fingerprint, seq DESC);
DROP INDEX extraction_anchor_rules_tenant_fingerprint_idx;

-- invoice_app becomes read-write here; 20260829082535 granted SELECT only. Still no UPDATE
-- and no DELETE: rules are append-only, and a wrong rule is superseded by a newer one, never
-- edited. nextval needs the sequence USAGE; the table grant does not carry it.
GRANT INSERT ON extraction_anchor_rules TO invoice_app;
GRANT USAGE ON SEQUENCE extraction_anchor_rules_seq_seq TO invoice_app;

-- +goose Down
REVOKE INSERT ON extraction_anchor_rules FROM invoice_app;

DROP INDEX extraction_anchor_rules_tenant_fingerprint_seq_idx;
-- Dropping the column drops its owned sequence, and the USAGE grant with it.
ALTER TABLE extraction_anchor_rules DROP COLUMN seq;
CREATE INDEX extraction_anchor_rules_tenant_fingerprint_idx
    ON extraction_anchor_rules (tenant_id, layout_fingerprint);

ALTER TABLE extraction_jobs DROP COLUMN layout_anchors;
ALTER TABLE extraction_jobs DROP COLUMN layout_fingerprint;
