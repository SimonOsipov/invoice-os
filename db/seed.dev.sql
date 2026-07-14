-- db/seed.dev.sql — LOCAL dev fixture data (M2-06). NOT a migration and NOT run in CI.
--
-- [dev-env deploy trigger 2026-07-14] No-op comment: this branch exists only to open a
-- ready PR so dev-env.yml redeploys the full fleet on current main (M4-02 merged). Revert
-- or close the PR to scale the dev env back to zero. No functional change.
--
-- `make dev-db` runs this in-container as the POSTGRES SUPERUSER, after migrations.
-- The superuser has BYPASSRLS, so these inserts need no app.current_tenant context and
-- no INSERT grant on the app role (invoice_app is deliberately SELECT-only on tenants).
-- Idempotent (ON CONFLICT DO NOTHING) so re-running `make dev-db` never errors.
--
-- The UUIDs are FIXED and well-known on purpose: mint a mock-issuer JWT (M2-05) with
-- app_metadata.tenant_id set to one of these and the whole auth → SET LOCAL → RLS path
-- resolves to a real seeded tenant, and M2-13's mock-login round trip has a row to read.
-- Tenant A/B exist so cross-tenant isolation can be exercised by hand and in M2-07; the
-- two persona tenants below back the M2-13 / task-21 sign-in personas (the frontend sends
-- their id as app_metadata.tenant_id), so /v1/me renders the real firm / in-house name.
--   1111… → Okafor & Partners  (persona: Chinedu Okafor, firm accountant)
--   2222… → Honeywell Group    (persona: Ngozi Balogun, in-house accountant)

-- kind is named explicitly (not left to the tenants.kind DEFAULT 'firm' from M3-01) and
-- the conflict clause is DO UPDATE so a local `make dev-db` re-run CORRECTS an
-- already-seeded row's kind (DO NOTHING would leave a stale kind in place).
INSERT INTO tenants (id, name, kind) VALUES
    ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'Tenant A (dev)',    'firm'),
    ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'Tenant B (dev)',    'firm'),
    ('11111111-1111-1111-1111-111111111111', 'Okafor & Partners', 'firm'),
    ('22222222-2222-2222-2222-222222222222', 'Honeywell Group',   'in_house')
ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind;

-- M3-02: demo firm memberships (all three roles) + the in-house persona's own membership
-- so her /me sign-in stays green now that /me is membership-gated (fail-closed 403
-- otherwise). Roles (admin/preparer/reviewer) already exist from the M3-01 migration.
INSERT INTO memberships (tenant_id, user_id, role) VALUES
    -- Okafor & Partners (kind='firm') — all three roles
    ('11111111-1111-1111-1111-111111111111', 'c0000000-0000-0000-0000-000000000001', 'admin'),     -- Chinedu Okafor (firm persona)
    ('11111111-1111-1111-1111-111111111111', 'c0000000-0000-0000-0000-000000000003', 'preparer'),  -- seed-only
    ('11111111-1111-1111-1111-111111111111', 'c0000000-0000-0000-0000-000000000004', 'reviewer'),  -- seed-only
    -- Honeywell Group (in-house persona)
    ('22222222-2222-2222-2222-222222222222', 'c0000000-0000-0000-0000-000000000002', 'admin')      -- Ngozi Balogun
ON CONFLICT (tenant_id, user_id) DO NOTHING;
