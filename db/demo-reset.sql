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

-- (M3-12-02 adds: DELETE demo portfolio + INSERT the 27 curated rows, before COMMIT.)

COMMIT;
