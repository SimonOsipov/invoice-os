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
-- Two tenants so cross-tenant isolation can be exercised by hand and in M2-07.

INSERT INTO tenants (id, name) VALUES
    ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'Tenant A (dev)'),
    ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'Tenant B (dev)')
ON CONFLICT (id) DO NOTHING;
