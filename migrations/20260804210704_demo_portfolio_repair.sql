-- Repairs already-deployed demo environments: withdraws the 17 clients
-- db/seed.dev.sql stopped declaring, and clears the stale seeded source-document
-- links so the demo-document seeder rebuilds them. An UPSERT-only seed can do neither.
--
-- Since DEMO-04 this is a no-op on the demo tenants: db.PurgeDemoTenants deletes
-- their rows before every seed, so there is nothing left for it to repair. Kept
-- because migrations are forward-only and it may still have work to do on a
-- database that has not yet booted a gateway carrying the purge.

-- +goose Up

-- Migrations run as invoice_migrator (NOBYPASSRLS) against FORCE'd RLS tables: without
-- this setting every statement below matches zero rows and goose still records the
-- migration applied. It is transaction-local, so this file must stay in one transaction.
SELECT set_config('app.current_tenant', '11111111-1111-1111-1111-111111111111', true);

-- The TIN list is the selector; the NOT EXISTS guards are the safety net -- invoices
-- would raise (RESTRICT) and import_batches would cascade the batch away silently.
DELETE FROM business_entities be
WHERE be.tenant_id = '11111111-1111-1111-1111-111111111111'
  AND be.tin IN (
        '10090123-0009', '10101234-0010', '10112345-0011', '10123456-0012',
        '10134567-0013', '10145678-0014', '10156789-0015', '10167890-0016',
        '10178901-0017', '10189012-0018', '10190123-0019', '10201234-0020',
        '10212345-0021', '10245678-0024', '10256789-0025', '10267890-0026',
        '10278901-0027'
      )
  AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.entity_id = be.id)
  AND NOT EXISTS (SELECT 1 FROM import_batches ib WHERE ib.entity_id = be.id);

SELECT set_config('app.current_tenant', '11111111-1111-1111-1111-111111111111', true);

-- Both columns together: invoices_source_rows_requires_document forbids
-- source_rows without a document.
UPDATE invoices
SET source_document_id = NULL,
    source_rows        = NULL
WHERE tenant_id = '11111111-1111-1111-1111-111111111111'
  AND invoice_number LIKE 'DEMO-2026-%'
  AND source_document_id IS NOT NULL;

-- RLS admits one tenant per setting, so the second demo tenant needs its own pair
-- of statements; a single tenant_id IN (...) would silently miss it.
SELECT set_config('app.current_tenant', '22222222-2222-2222-2222-222222222222', true);

UPDATE invoices
SET source_document_id = NULL,
    source_rows        = NULL
WHERE tenant_id = '22222222-2222-2222-2222-222222222222'
  AND invoice_number LIKE 'DEMO-2026-%'
  AND source_document_id IS NOT NULL;

-- +goose Down

SELECT set_config('app.current_tenant', '11111111-1111-1111-1111-111111111111', true);

-- Guarded on the demo tenant existing so the CI reset -> up round-trip, run against an
-- empty database, inserts nothing; the guard only reads correctly after the set_config
-- above. No inverse for the document unlink -- the previous links are not recorded.
INSERT INTO business_entities (tenant_id, name, tin, sector, status)
SELECT '11111111-1111-1111-1111-111111111111', v.name, v.tin, v.sector, v.status
FROM (VALUES
        ('Nwosu Foods & Beverages Ltd',      '10090123-0009', 'Food & Beverage',        'active'),
        ('Yakubu Motors Ltd',                '10101234-0010', 'Automotive',             'active'),
        ('Chidinma Cosmetics Ltd',           '10112345-0011', 'Cosmetics',              'active'),
        ('Obiora Steel Works Ltd',           '10123456-0012', 'Manufacturing',          'active'),
        ('Funmilayo Catering Services Ltd',  '10134567-0013', 'Catering',               'active'),
        ('Danjuma Petroleum Ltd',            '10145678-0014', 'Oil & Gas',              'active'),
        ('Ngozi Interiors Ltd',              '10156789-0015', 'Interior Design',        'active'),
        ('Uche Digital Solutions Ltd',       '10167890-0016', 'Technology',             'active'),
        ('Ibrahim Farms Ltd',                '10178901-0017', 'Agriculture',            'active'),
        ('Amara Publishing Ltd',             '10189012-0018', 'Publishing',             'active'),
        ('Tunde Electricals Ltd',            '10190123-0019', 'Electricals',            'active'),
        ('Kemi Beauty Concepts Ltd',         '10201234-0020', 'Beauty & Personal Care', 'active'),
        ('Segun Haulage Ltd',                '10212345-0021', 'Logistics',              'active'),
        ('Chinwe Poultry Farms Ltd',         '10245678-0024', 'Agriculture',            'archived'),
        ('Musa Hardware Stores Ltd',         '10256789-0025', 'Retail',                 'archived'),
        ('Bisi Event Planners Ltd',          '10267890-0026', 'Events',                 'archived'),
        ('Ekene Auto Parts Ltd',             '10278901-0027', 'Automotive',             'archived')
     ) AS v (name, tin, sector, status)
WHERE EXISTS (SELECT 1 FROM tenants WHERE id = '11111111-1111-1111-1111-111111111111')
ON CONFLICT (tenant_id, tin) WHERE tin IS NOT NULL DO NOTHING;
