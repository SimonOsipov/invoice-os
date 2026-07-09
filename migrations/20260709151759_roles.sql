-- M3-01: the `roles` global lookup table.
--
-- GLOBAL reference data, not tenant data: no tenant_id column, no RLS — every tenant
-- sees the same fixed set of membership roles (docs/migrations.md §3 scopes RLS to
-- tenant-owned tables only). Seeded here with the fixed role set membership rows
-- reference; invoice_app gets SELECT only (read-only, least privilege).

-- +goose Up
CREATE TABLE roles (
    name text PRIMARY KEY CHECK (name IN ('admin', 'preparer', 'reviewer'))
);

INSERT INTO roles (name) VALUES ('admin'), ('preparer'), ('reviewer');

GRANT SELECT ON roles TO invoice_app;

-- +goose Down
DROP TABLE roles;
