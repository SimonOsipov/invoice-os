-- db/seed.dev.sql — LOCAL dev fixture data (M2-06). NOT a migration and NOT run in CI.
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

-- task-162/M4-22-03: fold the former reset script's rule re-enable + curated
-- demo portfolio into the boot-time seed ([demo-seed-shape]). No DELETE is
-- ported here: a boot-time seed must stay destructive-statement-free
-- (TestSeedFileHasNoDestructiveStatements), and a fresh per-PR env has
-- nothing to clear anyway -- only CREATE, or REPAIR on a re-seed.
--
-- Rules are GLOBAL (no tenant_id, no RLS): restores any rule a prior demo
-- kill-switched (e.g. vat-standard-rate). Safe under the M4-17
-- rules_content_lock / M4-18 active-implies-sealed lock -- an enabled-only
-- UPDATE is the sanctioned M3-06 kill-switch carve-out; every other column
-- of a sealed rule set stays immutable.
UPDATE rules SET enabled = true WHERE enabled = false;

-- The 27 curated business_entities rows for the demo tenant (Okafor &
-- Partners, 21 active + 6 archived, [demo-seed-shape]). DO UPDATE, not DO
-- NOTHING, so a re-run REPAIRS a row a prior demo hand-edited back to its
-- curated name/status. Conflict target is the partial unique index
-- business_entities_tenant_tin_uq -- every row below has a distinct,
-- non-null TIN, so this always resolves to that index.
INSERT INTO business_entities (tenant_id, name, tin, status) VALUES
  ('11111111-1111-1111-1111-111111111111', 'Adeyemi & Sons Trading Ltd',       '10012345-0001', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Chukwu Global Ventures Ltd',       '10023456-0002', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Okonkwo Textiles Nigeria Ltd',     '10034567-0003', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Balogun Agro-Allied Ltd',          '10045678-0004', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Emeka Pharmaceuticals Ltd',        '10056789-0005', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Aliyu Logistics Services Ltd',     '10067890-0006', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Ifeoma Fashion House Ltd',         '10078901-0007', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Bello Construction Nigeria Ltd',   '10089012-0008', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Nwosu Foods & Beverages Ltd',      '10090123-0009', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Yakubu Motors Ltd',                '10101234-0010', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Chidinma Cosmetics Ltd',           '10112345-0011', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Obiora Steel Works Ltd',           '10123456-0012', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Funmilayo Catering Services Ltd',  '10134567-0013', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Danjuma Petroleum Ltd',            '10145678-0014', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Ngozi Interiors Ltd',              '10156789-0015', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Uche Digital Solutions Ltd',       '10167890-0016', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Ibrahim Farms Ltd',                '10178901-0017', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Amara Publishing Ltd',             '10189012-0018', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Tunde Electricals Ltd',            '10190123-0019', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Kemi Beauty Concepts Ltd',         '10201234-0020', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Segun Haulage Ltd',                '10212345-0021', 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Olumide Printing Press Ltd',       '10223456-0022', 'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Halima Boutique Ltd',              '10234567-0023', 'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Chinwe Poultry Farms Ltd',         '10245678-0024', 'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Musa Hardware Stores Ltd',         '10256789-0025', 'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Bisi Event Planners Ltd',          '10267890-0026', 'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Ekene Auto Parts Ltd',             '10278901-0027', 'archived')
ON CONFLICT (tenant_id, tin) WHERE tin IS NOT NULL
    DO UPDATE SET name = EXCLUDED.name, status = EXCLUDED.status;

