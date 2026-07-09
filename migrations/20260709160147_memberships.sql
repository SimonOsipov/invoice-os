-- M3-01-04: memberships — links a user to a tenant with a role (docs/migrations.md
-- §3, §8). `user_id` is the GoTrue JWT subject (a uuid) — deliberately NO FK: GoTrue
-- is a separate auth system, not a table in this DB, so there is no local `users`
-- table to reference.
--
-- This table is born with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (ENABLE + FORCE ROW LEVEL SECURITY, a permissive USING that doubles as the
-- INSERT/UPDATE WITH CHECK, tenant_id = the app.current_tenant GUC, fail-closed when
-- unset) — same shape as tenants/business_entities/audit_log/idempotency_keys.
--
-- Deliberately NO `tenant_enumerate`/invoice_tenant_reader policy — matches
-- business_entities/audit_log/idempotency_keys, not tenants. Least-privilege per
-- docs/migrations.md §3: nothing outside a tenant's own membership list has a
-- documented cross-tenant read need yet (QA-Verify F1 conservative-default — grant
-- reader access later, in the migration that introduces the actual need, not
-- preemptively here).
--
-- `role` references roles(name) (M3-01, rows: admin, preparer, reviewer) — an
-- unrecognized role is refused with a foreign-key violation, not a CHECK.
--
-- UNIQUE (tenant_id, user_id): one membership per user per tenant — a user gets at
-- most one role in a given tenant.
--
-- FK to tenants(id) ON DELETE CASCADE: memberships are genuinely owned by a tenant
-- row and should not be able to outlive it, same rationale as business_entities.
--
-- tenant_id has NO GUC default (unlike audit_log): it is a plain caller-supplied
-- NOT NULL column, not an implicit-actor ledger — the caller always knows which
-- tenant's membership it is inserting into.
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE memberships (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL,
    role       text        NOT NULL REFERENCES roles(name),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT memberships_tenant_user_uq UNIQUE (tenant_id, user_id)
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policy below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE memberships FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees
-- (and can insert/update) a memberships row only when app.current_tenant equals its
-- tenant_id; an unset GUC → NULL → no rows. This USING doubles as the INSERT/UPDATE
-- WITH CHECK, so both a cross-tenant INSERT and a same-row reassignment to another
-- tenant are refused (42501).
CREATE POLICY tenant_isolation ON memberships
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating
-- migration, never blanket). invoice_app: full DML — this is the membership CRUD
-- surface the app owns. No grant to invoice_tenant_reader (see header).
GRANT SELECT, INSERT, UPDATE, DELETE ON memberships TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy and grants with it, so reset→up round-trips
-- clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE memberships;
