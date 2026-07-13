-- db/demo-reset.sql — restore the demo tenant to a clean, presentable state (M3-12).
-- Data-only, superuser (BYPASSRLS), idempotent. Applied via: make demo-reset
-- Single transaction: a guard RAISE rolls back everything (nothing half-applied).
-- Sibling of db/reset.dev.sql / db/seed.dev.sql. NOT a migration. Must stay parameter-free.
BEGIN;

-- Guard (AC-7): refuse unless the seeded demo/dev fixture is present. The demo tenant
-- is a dev-only seed (db/seed.dev.sql), absent from all migrations, so its presence
-- positively identifies the demo DB; its absence aborts + rolls back with zero writes.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM tenants
    WHERE id = '11111111-1111-1111-1111-111111111111'
      AND name = 'Okafor & Partners'
      AND kind = 'firm'
  ) THEN
    RAISE EXCEPTION 'demo-reset refused: target is not the seeded demo/dev DB (demo tenant fixture 11111111-…/''Okafor & Partners'' not found)';
  END IF;
END $$;

-- Rules re-enable (AC-3): rules are GLOBAL (no tenant_id, no RLS). Restore any rule a
-- prior demo kill-switched (e.g. vat-standard-rate) to the seeded default. Idempotent.
UPDATE rules SET enabled = true WHERE enabled = false;

-- Clear + curate the demo portfolio (AC-2/4/5/6): DELETE-then-INSERT converges to
-- exactly the 27 curated rows every run, clearing any accumulated Demo Client /
-- Demo Onboarding / extra-archived junk. Scoped by tenant_id → other tenants untouched.
DELETE FROM business_entities WHERE tenant_id = '11111111-1111-1111-1111-111111111111';
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
  ('11111111-1111-1111-1111-111111111111', 'Ekene Auto Parts Ltd',             '10278901-0027', 'archived');

COMMIT;
