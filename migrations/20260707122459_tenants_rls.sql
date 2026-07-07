-- First real table + the FORCE-RLS tenant-isolation template (M2-06).
--
-- `tenants` is the root of the tenancy model and the worked example every future
-- tenant-scoped table copies: ENABLE + FORCE ROW LEVEL SECURITY, a permissive
-- isolation policy equating the tenant-key column to the app.current_tenant GUC, and
-- explicit least-privilege grants (docs/migrations.md §3, §4). `tenants` is the one
-- self-referential case — its own `id` IS the tenant key; every downstream table
-- uses a `tenant_id` column instead, but the policy shape is otherwise identical.
--
-- Why FORCE: without it the table OWNER (invoice_migrator) bypasses RLS. FORCE makes
-- the owner subject to the policies too, so isolation holds for every non-superuser
-- role (M2-07 proves the owner-bypass case adversarially).
--
-- gen_random_uuid() is core on PG13+ (no extension). The isolation predicate reads the
-- GUC missing_ok (current_setting(...,true) → NULL when unset → zero rows: fail-closed),
-- and nullif(...,'') maps an empty-string GUC to NULL so `''::uuid` can never raise.

-- +goose Up
CREATE TABLE tenants (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policies below — enable alone would let the owner bypass them.
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role (incl. the reader).
-- A connection sees a tenant row only when app.current_tenant equals its id; an unset
-- GUC → NULL → no rows. This USING doubles as the INSERT/UPDATE WITH CHECK.
CREATE POLICY tenant_isolation ON tenants
    USING (id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Enumeration policy — the ONE cross-tenant read path, scoped to invoice_tenant_reader
-- only. RLS combines permissive policies with OR, so for the reader the predicate is
-- (id = current_tenant) OR true = every row; invoice_app is not in this policy's TO
-- list, so it still sees only its current tenant. No BYPASSRLS anywhere.
CREATE POLICY tenant_enumerate ON tenants
    FOR SELECT TO invoice_tenant_reader
    USING (true);

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating migration,
-- never blanket). invoice_app: SELECT only — nothing writes tenants yet (provisioning
-- is a later story; the dev seed runs as the superuser). invoice_tenant_reader: SELECT,
-- the privilege its enumeration policy needs.
GRANT SELECT ON tenants TO invoice_app;
GRANT SELECT ON tenants TO invoice_tenant_reader;

-- +goose Down
-- Dropping the table removes its policies and grants with it, so reset→up round-trips
-- clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE tenants;
