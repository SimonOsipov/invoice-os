-- db/demo-reset.sql — guarded, idempotent, single-transaction reset of the demo tenant
-- (Okafor & Partners) to a curated, deterministic state (M3-12).
--
-- STUB: implemented by M3-12-01/02. This placeholder is a valid no-op transaction so
-- the DB-backed TestRLS_DemoReset_* suite (internal/platform/db/demo_reset_test.go)
-- compiles and applies cleanly, but is intentionally RED against it: no guard, no rule
-- re-enable, no clear/curate. [M3-12-01] adds the guard (DO $$ … RAISE EXCEPTION …$$)
-- and the global `UPDATE rules SET enabled = true WHERE enabled = false;`; [M3-12-02]
-- adds the demo-tenant business_entities clear + curate.

BEGIN;
COMMIT;
