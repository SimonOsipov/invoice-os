-- customers: the per-tenant buyer directory; business_entities gains the NRS seller contact columns.

-- +goose Up
CREATE TABLE customers (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    tin            text        NOT NULL CHECK (tin ~ '^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$'),
    name           text,
    contact_person text,
    buyer_type     text        CHECK (buyer_type IN ('B2B', 'B2G', 'B2C')),
    email          text,
    telephone      text,
    street         text,
    city           text,
    postal_zone    text,
    country        text,
    state          text,
    lga            text,
    edited_fields  text[]      NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT customers_tenant_tin_uq UNIQUE (tenant_id, tin)
);

ALTER TABLE customers ENABLE ROW LEVEL SECURITY;
ALTER TABLE customers FORCE  ROW LEVEL SECURITY;

CREATE POLICY tenant_isolation ON customers
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- No DELETE, no reader grant.
GRANT SELECT, INSERT, UPDATE ON customers TO invoice_app;

ALTER TABLE business_entities
    ADD COLUMN email text, ADD COLUMN telephone text, ADD COLUMN street text, ADD COLUMN city text,
    ADD COLUMN postal_zone text, ADD COLUMN country text, ADD COLUMN state text, ADD COLUMN lga text;

-- +goose Down
ALTER TABLE business_entities
    DROP COLUMN email, DROP COLUMN telephone, DROP COLUMN street, DROP COLUMN city,
    DROP COLUMN postal_zone, DROP COLUMN country, DROP COLUMN state, DROP COLUMN lga;

DROP TABLE customers;
