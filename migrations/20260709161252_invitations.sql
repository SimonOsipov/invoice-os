-- M3-01-05: invitations — a minimal pending-invite table (docs/migrations.md §3,
-- §8). tenant_id + role (FK to roles(name), same as memberships) + invitee_email, a
-- pre-auth string for someone who does not have a GoTrue account yet — there is no
-- local `users` row to FK to at invite time, same rationale as memberships.user_id
-- having no FK.
--
-- This table is born with the verbatim M2-06 FORCE-RLS `tenant_isolation` template
-- (ENABLE + FORCE ROW LEVEL SECURITY, a permissive USING that doubles as the
-- INSERT/UPDATE WITH CHECK, tenant_id = the app.current_tenant GUC, fail-closed when
-- unset) — same shape as tenants/business_entities/audit_log/idempotency_keys/
-- memberships.
--
-- Deliberately NO `tenant_enumerate`/invoice_tenant_reader policy — matches
-- memberships/business_entities/audit_log/idempotency_keys, not tenants (QA-Verify
-- F1 conservative-default — grant reader access later, in the migration that
-- introduces the actual cross-tenant need, not preemptively here).
--
-- status default 'pending', CHECK'd to ('pending', 'accepted', 'revoked'). Grants are
-- SELECT, INSERT, UPDATE only — deliberately NO DELETE (QA-Verify F5): revoking an
-- invite is an UPDATE to status = 'revoked', not a row deletion, so the app never
-- needs DELETE on this table.
--
-- Partial UNIQUE (tenant_id, invitee_email) WHERE status = 'pending': at most one
-- pending invite per (tenant, invitee) at a time. Once a pending row moves to
-- accepted/revoked it drops out of the partial index, so the same invitee can be
-- invited again.
--
-- Out of scope for this migration (per the Test Spec): no invitation token, no
-- email-delivery columns, no expiry — those belong to a later story once the
-- delivery mechanism is designed.
--
-- FK to tenants(id) ON DELETE CASCADE: invitations are genuinely owned by a tenant
-- row and should not be able to outlive it, same rationale as business_entities/
-- memberships.
--
-- tenant_id has NO GUC default (unlike audit_log): it is a plain caller-supplied
-- NOT NULL column, not an implicit-actor ledger — the caller always knows which
-- tenant it is inviting into.
--
-- No StatementBegin/End: no function bodies in this migration.

-- +goose Up
CREATE TABLE invitations (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    role          text        NOT NULL REFERENCES roles(name),
    invitee_email text        NOT NULL CHECK (char_length(invitee_email) > 0),
    status        text        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'accepted', 'revoked')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Enable AND force: force is what subjects the table owner (the migrator) to the
-- policy below — enable alone would let the owner bypass it (docs/migrations.md §1).
ALTER TABLE invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE invitations FORCE  ROW LEVEL SECURITY;

-- Isolation policy — no TO clause, so it applies to EVERY role. A connection sees
-- (and can insert/update) an invitations row only when app.current_tenant equals its
-- tenant_id; an unset GUC → NULL → no rows. This USING doubles as the INSERT/UPDATE
-- WITH CHECK, so both a cross-tenant INSERT and a same-row reassignment to another
-- tenant are refused (42501).
CREATE POLICY tenant_isolation ON invitations
    USING (tenant_id = nullif(current_setting('app.current_tenant', true), '')::uuid);

-- At most one PENDING invite per (tenant, invitee) — partial index so an
-- accepted/revoked invite does not block re-inviting the same email later.
CREATE UNIQUE INDEX invitations_tenant_invitee_pending_uq
    ON invitations (tenant_id, invitee_email) WHERE status = 'pending';

-- Least-privilege grants, per docs/migrations.md §3 (granted in the creating
-- migration, never blanket). invoice_app: SELECT/INSERT/UPDATE only — deliberately
-- NO DELETE (see header: revoked status is the removal path). No grant to
-- invoice_tenant_reader (see header).
GRANT SELECT, INSERT, UPDATE ON invitations TO invoice_app;

-- +goose Down
-- Dropping the table removes its policy, index, and grants with it, so reset→up
-- round-trips clean (the CI reversibility gate, docs/migrations.md §6).
DROP TABLE invitations;
