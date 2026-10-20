-- +goose Up
CREATE TABLE rollup_snapshots (
    tenant_id       uuid    NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    entity_id       uuid    NOT NULL,
    day             date    NOT NULL, -- West Africa Time date of the run
    total           integer NOT NULL,
    readiness_num   integer NOT NULL,
    needs_attention integer NOT NULL,
    not_submitted   integer NOT NULL, -- draft + validated
    vat_kobo        bigint  NOT NULL,
    CONSTRAINT rollup_snapshots_pk PRIMARY KEY (tenant_id, entity_id, day),
    -- Composite: RI checks bypass RLS.
    CONSTRAINT rollup_snapshots_tenant_entity_fk FOREIGN KEY (tenant_id, entity_id)
        REFERENCES business_entities (tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE rollup_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE rollup_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rollup_snapshots USING (
    tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid
    AND (coalesce(current_setting('app.entity_scope', true), '') IN ('', '*')
         OR entity_id = ANY (nullif(nullif(current_setting('app.entity_scope', true), ''), '*')::uuid[])));
-- Upsert and read only: no DELETE.
GRANT SELECT, INSERT, UPDATE ON rollup_snapshots TO invoice_app;

-- +goose Down
DROP TABLE rollup_snapshots;
