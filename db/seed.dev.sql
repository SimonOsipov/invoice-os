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
-- curated name/sector/status. Conflict target is the partial unique index
-- business_entities_tenant_tin_uq -- every row below has a distinct,
-- non-null TIN, so this always resolves to that index.
--
-- sector (persona-handoff-fix step 5, Task B): the column is
-- nullable free text (migrations/20260709155011_business_entities.sql) with
-- no dropdown/enum anywhere in the app (lib/entityForm.ts treats it as a
-- plain string; ClientsView.tsx just renders it) -- these are display-ready,
-- human-readable values matched to each company's own name, NOT the
-- frontend's SectorKey mock-generator vocabulary (logistics/foods/oilfield/
-- trading/manufacturing/textile, types.ts), which would render literally
-- (lowercase) if reused here. DO UPDATE backfills sector onto rows a
-- pre-this-story deploy already inserted with sector NULL (the old insert
-- list was (tenant_id, name, tin, status) only) -- a DO NOTHING here would
-- leave those NULL forever on every PR/dev env created before this change.
INSERT INTO business_entities (tenant_id, name, tin, sector, status) VALUES
  ('11111111-1111-1111-1111-111111111111', 'Adeyemi & Sons Trading Ltd',       '10012345-0001', 'Trading',                  'active'),
  ('11111111-1111-1111-1111-111111111111', 'Chukwu Global Ventures Ltd',       '10023456-0002', 'Trading',                  'active'),
  ('11111111-1111-1111-1111-111111111111', 'Okonkwo Textiles Nigeria Ltd',     '10034567-0003', 'Textiles',                 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Balogun Agro-Allied Ltd',          '10045678-0004', 'Agriculture',              'active'),
  ('11111111-1111-1111-1111-111111111111', 'Emeka Pharmaceuticals Ltd',        '10056789-0005', 'Pharmaceuticals',          'active'),
  ('11111111-1111-1111-1111-111111111111', 'Aliyu Logistics Services Ltd',     '10067890-0006', 'Logistics',                'active'),
  ('11111111-1111-1111-1111-111111111111', 'Ifeoma Fashion House Ltd',         '10078901-0007', 'Fashion',                  'active'),
  ('11111111-1111-1111-1111-111111111111', 'Bello Construction Nigeria Ltd',   '10089012-0008', 'Construction',             'active'),
  ('11111111-1111-1111-1111-111111111111', 'Nwosu Foods & Beverages Ltd',      '10090123-0009', 'Food & Beverage',          'active'),
  ('11111111-1111-1111-1111-111111111111', 'Yakubu Motors Ltd',                '10101234-0010', 'Automotive',               'active'),
  ('11111111-1111-1111-1111-111111111111', 'Chidinma Cosmetics Ltd',           '10112345-0011', 'Cosmetics',                'active'),
  ('11111111-1111-1111-1111-111111111111', 'Obiora Steel Works Ltd',           '10123456-0012', 'Manufacturing',            'active'),
  ('11111111-1111-1111-1111-111111111111', 'Funmilayo Catering Services Ltd',  '10134567-0013', 'Catering',                 'active'),
  ('11111111-1111-1111-1111-111111111111', 'Danjuma Petroleum Ltd',            '10145678-0014', 'Oil & Gas',                'active'),
  ('11111111-1111-1111-1111-111111111111', 'Ngozi Interiors Ltd',              '10156789-0015', 'Interior Design',          'active'),
  ('11111111-1111-1111-1111-111111111111', 'Uche Digital Solutions Ltd',       '10167890-0016', 'Technology',               'active'),
  ('11111111-1111-1111-1111-111111111111', 'Ibrahim Farms Ltd',                '10178901-0017', 'Agriculture',              'active'),
  ('11111111-1111-1111-1111-111111111111', 'Amara Publishing Ltd',             '10189012-0018', 'Publishing',               'active'),
  ('11111111-1111-1111-1111-111111111111', 'Tunde Electricals Ltd',            '10190123-0019', 'Electricals',              'active'),
  ('11111111-1111-1111-1111-111111111111', 'Kemi Beauty Concepts Ltd',         '10201234-0020', 'Beauty & Personal Care',   'active'),
  ('11111111-1111-1111-1111-111111111111', 'Segun Haulage Ltd',                '10212345-0021', 'Logistics',                'active'),
  ('11111111-1111-1111-1111-111111111111', 'Olumide Printing Press Ltd',       '10223456-0022', 'Printing',                 'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Halima Boutique Ltd',              '10234567-0023', 'Retail',                   'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Chinwe Poultry Farms Ltd',         '10245678-0024', 'Agriculture',              'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Musa Hardware Stores Ltd',         '10256789-0025', 'Retail',                   'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Bisi Event Planners Ltd',          '10267890-0026', 'Events',                   'archived'),
  ('11111111-1111-1111-1111-111111111111', 'Ekene Auto Parts Ltd',             '10278901-0027', 'Automotive',               'archived')
ON CONFLICT (tenant_id, tin) WHERE tin IS NOT NULL
    DO UPDATE SET name = EXCLUDED.name, sector = EXCLUDED.sector, status = EXCLUDED.status;

-- task-304 (INVCR-01-19): ONE business_entities row for the in-house tenant
-- (Honeywell Group, 2222...) -- previously zero, which was the second half of why
-- an in-house sign-in could not file anything at all (the first half was
-- frontend/app/src/App.tsx's `active` memo, which special-cased mode==='inhouse' to
-- a synthetic Client with entityId:null and never consulted the fetched entity list
-- even once a row existed here -- both are fixed by this story, together).
--
-- In-house is a DEGENERATE case of the firm model (story description's Design
-- section): a firm manages many client entities, a company that files for itself
-- owns exactly one. This block therefore mirrors the firm block above in STYLE
-- (raw SQL, ON CONFLICT DO UPDATE idempotency, same partial unique index target)
-- but seeds exactly ONE row, never a portfolio.
--
-- TIN '20665510-0001' is the same literal src/data.tsx's mock CFG entry for
-- "Honeywell Group" already uses as its demo profile's tin (looked up there by
-- name, lib/clients.ts's buildClientForEntity) -- reusing it keeps the mock overlay
-- and the real seeded row telling the same story rather than two different TINs
-- for "the same" company. It is a hyphenated NNNNNNNN-NNNN literal: raw SQL
-- bypasses portfolio.ValidateTIN, so no Luhn check digit is needed here, and this
-- is deliberately NOT the 10-digit JTB shape MBSSupplierTIN leaves alone
-- (internal/invoice/supplier_tin.go) -- it is the same MBS wire spelling the firm
-- block's 27 rows above already use, so supplier-tin-format never fires against it.
INSERT INTO business_entities (tenant_id, name, tin, sector, status) VALUES
  ('22222222-2222-2222-2222-222222222222', 'Honeywell Group', '20665510-0001', 'Manufacturing', 'active')
ON CONFLICT (tenant_id, tin) WHERE tin IS NOT NULL
    DO UPDATE SET name = EXCLUDED.name, sector = EXCLUDED.sector, status = EXCLUDED.status;

-- persona-handoff-fix step 4 ([demo-invoice-seed]): a curated invoice history for 6 of
-- the 27 business_entities above, so the entity-scoped surfaces persona-handoff-fix
-- steps 1-3 shipped (workspace switcher, Overview, Invoices, Customers, Reports --
-- 13e55b6/3f60a3b/63b2fbe) have something real to scope TO. Before this block every one
-- of the 27 entities had zero invoices, so selecting ANY of them rendered an honest but
-- useless empty state -- the dev/PR environments' only invoice rows were incidental
-- residue from whichever e2e spec happened to create some as a side effect of its own
-- assertions.
--
-- [default-entity-needs-data]: "Adeyemi & Sons Trading Ltd" (10012345-0001) is not just
-- one of the six -- it is the alphabetically-first row business_entities returns
-- (portfolio store.go's List, `ORDER BY name ASC, id ASC`, no status filter on the SPA's
-- own listEntities call), which makes it `clients[0]` in frontend/app/src/App.tsx's
-- `active` memo -- the workspace switcher's DEFAULT selection on a fresh sign-in, before
-- any explicit pick (and what e2e/topology/import-wizard.spec.ts's mixed-fixture test
-- lands on too, since it never touches the switcher). Leaving it empty would mean the
-- first-touch view of Overview/Invoices is STILL a blank slate; seeding it is what makes
-- that default view honest.
--
-- Entity ids are GENERATED (see the block above) -- these INSERTs resolve entity_id by
-- joining business_entities on its stable, curated TIN, never a literal uuid.
-- rule_set_version_id resolves the same way, via `(SELECT id FROM rule_set_versions
-- WHERE is_active)` (currently v3, migrations/20260731090000_rule_set_v3.sql; previously
-- v2, migrations/20260716185106_rule_set_v2.sql) -- never a literal, so this seed tracks
-- whichever version is active without a hand-maintained number. `validated` (a seed-only
-- column below, not a real one) gates whether a row
-- stamps rule_set_version_id at all: false for the 3 invoices left genuinely untouched
-- since creation (rule_set_version_id stays NULL, matching Store.Create's own invariant
-- that a fresh invoice starts unvalidated); true for every other row, including the ones
-- the gate blocked and left `draft` (a blocked validate call stamps rule_set_version_id
-- too -- internal/invoice/gate.go).
--
-- Idempotent DO UPDATE, not DO NOTHING, matching the entities block's own rationale: a
-- re-run (every deploy, [demo-seed-shape]) REPAIRS a hand-edited demo row rather than
-- leaving it drifted. Conflict target is invoices_tenant_entity_number_uq (tenant_id,
-- entity_id, invoice_number) -- every invoice_number below is additionally globally
-- unique tenant-wide (the DEMO-2026-#### prefix, never reused by any real import or e2e
-- fixture, which all mint INV-* numbers), so the line_items/history INSERTs further down
-- can join back on invoice_number alone with no ambiguity.
--
-- Violations are hand-picked to fire EXACTLY the rule they are meant to demonstrate, not
-- a wall of unrelated failures (an all-failing portfolio is as dishonest a demo as an
-- all-empty one): vat-standard-rate (wrong VAT on an otherwise-clean invoice, x2),
-- line-items-sum-subtotal (line rows don't reconcile to the stated subtotal, x1),
-- supplier-tin-format / buyer-tin-format (one malformed TIN on an otherwise-clean
-- invoice, x1 each) -- all four keys and their exact messages are copied verbatim from
-- the seeded rule set (migrations/20260711121327_seed_mbs_v1.sql), never invented. The
-- two `rejected` invoices carry the real mock-adapter rejection shape verbatim
-- (internal/submission/mock_script.go's mockRejectionReasons: code NGE-4102, message
-- "Customer tax identifier is not registered with the tax authority.", path
-- "buyer.tin") -- a real authority-level rejection, not a validation failure, so their
-- own `violations` stay `[]`.
--
-- DEMO-2026-1007..1009 carry the mock adapter's reserved trigger buyer TINs and stay
-- `validated`, so a demo can submit them live rather than only see the aftermath.
--
-- 31 invoices across 6 entities (Adeyemi/Chukwu/Okonkwo/Balogun/Emeka/Aliyu), comfortably
-- under listInvoices' server-side default page size of 50 (internal/invoice/handlers.go,
-- [D8]) -- ReportsView sums over that one page, tenant-wide, so this stays well clear of
-- the point where its KPIs would silently go partial as other specs' own fixtures
-- accumulate alongside it. The other 21 entities (15 active + 6 archived) are LEFT EMPTY
-- on purpose, so "no invoices yet" stays a reachable, honest state on this fleet, not
-- just a code path nothing ever exercises.
--
-- Nothing is left `queued` or `submitted`: the SPA polls those two statuses every 2s
-- (lib/invoices.ts's isInFlight) and no job here would ever advance them. The four that
-- were in flight are `failed` -- they never got a verdict, and inventing an authority
-- rejection for an invoice that was never sent is the dishonesty this seed is removing.
WITH invoice_seed (
    tin, invoice_number, status, issue_date, supplier_tin, supplier_name,
    buyer_tin, buyer_name, subtotal, vat, total, validated, violations, rejection_reasons
) AS (
  VALUES
    -- Adeyemi & Sons Trading Ltd (10012345-0001) -- [default-entity-needs-data]: mostly
    -- healthy, one flagged invoice and one that never got a verdict, spanning draft ->
    -- accepted.
    ('10012345-0001', 'DEMO-2026-1001', 'accepted',  '2026-06-02', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)', 500000.00, 37500.00, 537500.00, true,  '[]', '[]'),
    ('10012345-0001', 'DEMO-2026-1002', 'accepted',  '2026-06-05', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)', 220000.00, 16500.00, 236500.00, true,  '[]', '[]'),
    ('10012345-0001', 'DEMO-2026-1003', 'validated', '2026-06-10', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '20022233-0002', 'Lagos Textiles Mart (validated, ready to submit)',            180000.00, 13500.00, 193500.00, true,  '[]', '[]'),
    ('10012345-0001', 'DEMO-2026-1004', 'failed',    '2026-06-14', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '20033344-0003', 'Kano Agro Distributors (sent, no verdict)',                   95000.00,  7125.00,  102125.00, true,  '[]', '[]'),
    ('10012345-0001', 'DEMO-2026-1005', 'draft',     '2026-06-18', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '20044455-0004', 'Ibadan Consumer Goods Ltd (draft, not yet validated)',        60000.00,  4500.00,  64500.00,  false, '[]', '[]'),
    ('10012345-0001', 'DEMO-2026-1006', 'draft',     '2026-06-20', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '20055566-0005', 'Port Harcourt Marine Supplies Ltd (VAT off the standard rate)', 75000.00,  7500.00,  82500.00,  true,
      '[{"rule_key":"vat-standard-rate","severity":"error","message":"VAT must equal 7.5% of the subtotal."}]', '[]'),
    -- Reserved trigger buyer TINs, left `validated` so a demo can submit them and watch the
    -- live outcome. No seeded job or evidence: they have never been submitted.
    ('10012345-0001', 'DEMO-2026-1007', 'validated', '2026-06-26', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '99999999-0001', 'Sandbox APP (always accepts)',           120000.00, 9000.00,  129000.00, true,  '[]', '[]'),
    ('10012345-0001', 'DEMO-2026-1008', 'validated', '2026-06-27', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '99999999-0002', 'Sandbox APP (always rejects)',            96000.00, 7200.00,  103200.00, true,  '[]', '[]'),
    ('10012345-0001', 'DEMO-2026-1009', 'validated', '2026-06-28', '10012345-0001', 'Adeyemi & Sons Trading Ltd', '99999999-0003', 'Sandbox APP (defers, then accepts)',     144000.00, 10800.00, 154800.00, true,  '[]', '[]'),

    -- Chukwu Global Ventures Ltd (10023456-0002) -- the late-lifecycle failures, plus one
    -- blocked draft.
    ('10023456-0002', 'DEMO-2026-2001', 'accepted',  '2026-06-03', '10023456-0002', 'Chukwu Global Ventures Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)',    310000.00, 23250.00, 333250.00, true, '[]', '[]'),
    ('10023456-0002', 'DEMO-2026-2002', 'failed',    '2026-06-09', '10023456-0002', 'Chukwu Global Ventures Ltd', '20033344-0003', 'Kano Agro Distributors (sent, no verdict)',                     128000.00, 9600.00,  137600.00, true, '[]', '[]'),
    ('10023456-0002', 'DEMO-2026-2003', 'failed',    '2026-06-16', '10023456-0002', 'Chukwu Global Ventures Ltd', '20033344-0003', 'Kano Agro Distributors (sent, no verdict)',                     84000.00,  6300.00,  90300.00,  true, '[]', '[]'),
    ('10023456-0002', 'DEMO-2026-2004', 'failed',    '2026-06-21', '10023456-0002', 'Chukwu Global Ventures Ltd', '20033344-0003', 'Kano Agro Distributors (sent, no verdict)',                     45000.00,  3375.00,  48375.00,  true, '[]', '[]'),
    ('10023456-0002', 'DEMO-2026-2005', 'draft',     '2026-06-24', '10023456-0002', 'Chukwu Global Ventures Ltd', '20055566-0005', 'Port Harcourt Marine Supplies Ltd (VAT off the standard rate)', 66000.00,  6600.00,  72600.00,  true,
      '[{"rule_key":"vat-standard-rate","severity":"error","message":"VAT must equal 7.5% of the subtotal."}]', '[]'),

    -- Okonkwo Textiles Nigeria Ltd (10034567-0003) -- one authority rejection.
    ('10034567-0003', 'DEMO-2026-3001', 'accepted',  '2026-06-04', '10034567-0003', 'Okonkwo Textiles Nigeria Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)', 265000.00, 19875.00, 284875.00, true,  '[]', '[]'),
    ('10034567-0003', 'DEMO-2026-3002', 'accepted',  '2026-06-08', '10034567-0003', 'Okonkwo Textiles Nigeria Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)', 142000.00, 10650.00, 152650.00, true,  '[]', '[]'),
    ('10034567-0003', 'DEMO-2026-3003', 'rejected',  '2026-06-13', '10034567-0003', 'Okonkwo Textiles Nigeria Ltd', '20077788-0007', 'Enugu Metal Works Ltd (rejected by the authority)',          88000.00,  6600.00,  94600.00,  true,
      '[]', '[{"code":"NGE-4102","message":"Customer tax identifier is not registered with the tax authority.","path":"buyer.tin"}]'),
    ('10034567-0003', 'DEMO-2026-3004', 'validated', '2026-06-19', '10034567-0003', 'Okonkwo Textiles Nigeria Ltd', '20022233-0002', 'Lagos Textiles Mart (validated, ready to submit)',           51000.00,  3825.00,  54825.00,  true,  '[]', '[]'),
    ('10034567-0003', 'DEMO-2026-3005', 'draft',     '2026-06-24', '10034567-0003', 'Okonkwo Textiles Nigeria Ltd', '20044455-0004', 'Ibadan Consumer Goods Ltd (draft, not yet validated)',       39000.00,  2925.00,  41925.00,  false, '[]', '[]'),

    -- Balogun Agro-Allied Ltd (10045678-0004) -- one line-items/subtotal mismatch.
    ('10045678-0004', 'DEMO-2026-4001', 'accepted',  '2026-06-06', '10045678-0004', 'Balogun Agro-Allied Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)', 198000.00, 14850.00, 212850.00, true,  '[]', '[]'),
    ('10045678-0004', 'DEMO-2026-4002', 'draft',     '2026-06-11', '10045678-0004', 'Balogun Agro-Allied Ltd', '20066677-0006', 'Abuja Office Interiors Ltd (lines do not sum to subtotal)', 100000.00, 7500.00,  107500.00, true,
      '[{"rule_key":"line-items-sum-subtotal","severity":"error","message":"Line item amounts must sum to the invoice subtotal."}]', '[]'),
    ('10045678-0004', 'DEMO-2026-4003', 'validated', '2026-06-17', '10045678-0004', 'Balogun Agro-Allied Ltd', '20022233-0002', 'Lagos Textiles Mart (validated, ready to submit)',          62000.00,  4650.00,  66650.00,  true,  '[]', '[]'),
    ('10045678-0004', 'DEMO-2026-4004', 'draft',     '2026-06-22', '10045678-0004', 'Balogun Agro-Allied Ltd', '20044455-0004', 'Ibadan Consumer Goods Ltd (draft, not yet validated)',      40000.00,  3000.00,  43000.00,  false, '[]', '[]'),

    -- Emeka Pharmaceuticals Ltd (10056789-0005) -- clean validation record; its one
    -- needs_attention row is a transport failure, not a rule violation.
    ('10056789-0005', 'DEMO-2026-5001', 'accepted',  '2026-06-07', '10056789-0005', 'Emeka Pharmaceuticals Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)', 145000.00, 10875.00, 155875.00, true, '[]', '[]'),
    ('10056789-0005', 'DEMO-2026-5002', 'accepted',  '2026-06-12', '10056789-0005', 'Emeka Pharmaceuticals Ltd', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)', 210000.00, 15750.00, 225750.00, true, '[]', '[]'),
    ('10056789-0005', 'DEMO-2026-5003', 'validated', '2026-06-19', '10056789-0005', 'Emeka Pharmaceuticals Ltd', '20022233-0002', 'Lagos Textiles Mart (validated, ready to submit)',          76000.00,  5700.00,  81700.00,  true, '[]', '[]'),
    ('10056789-0005', 'DEMO-2026-5004', 'failed',    '2026-06-25', '10056789-0005', 'Emeka Pharmaceuticals Ltd', '20033344-0003', 'Kano Agro Distributors (sent, no verdict)',                 33000.00,  2475.00,  35475.00,  true, '[]', '[]'),
    ('10056789-0005', 'DEMO-2026-5005', 'draft',     '2026-06-25', '10056789-0005', 'Emeka Pharmaceuticals Ltd', '20044455-0004', 'Ibadan Consumer Goods Ltd (draft, not yet validated)',      48000.00,  3600.00,  51600.00,  false, '[]', '[]'),

    -- Aliyu Logistics Services Ltd (10067890-0006) -- the problem client: rejected +
    -- two malformed-TIN blocked drafts (needs_attention:3, the highest of the six).
    ('10067890-0006', 'DEMO-2026-6001', 'rejected', '2026-06-08', '10067890-0006', 'Aliyu Logistics Services Ltd', '20077788-0007', 'Enugu Metal Works Ltd (rejected by the authority)', 72000.00, 5400.00, 77400.00, true,
      '[]', '[{"code":"NGE-4102","message":"Customer tax identifier is not registered with the tax authority.","path":"buyer.tin"}]'),
    -- Supplier TIN mistyped on THIS invoice only (the entity's own TIN stays correct in
    -- business_entities above -- store-invalid-faithfully, invoices carry no CHECK on
    -- this column, migrations/20260714103137_invoices.sql's header).
    ('10067890-0006', 'DEMO-2026-6002', 'draft', '2026-06-15', 'BADTIN', 'Aliyu Logistics Services Ltd', '20088899-0008', 'Jos Highland Farms Ltd (supplier TIN malformed)', 54000.00, 4050.00, 58050.00, true,
      '[{"rule_key":"supplier-tin-format","severity":"error","message":"Supplier TIN must be in the format NNNNNNNN-NNNN (8 digits, hyphen, 4 digits).","path":"supplier.tin"}]', '[]'),
    ('10067890-0006', 'DEMO-2026-6003', 'draft', '2026-06-23', '10067890-0006', 'Aliyu Logistics Services Ltd', '12345678', 'Calabar Marine Services Ltd (buyer TIN malformed)', 47000.00, 3525.00, 50525.00, true,
      '[{"rule_key":"buyer-tin-format","severity":"error","message":"Buyer TIN, when present, must be in the format NNNNNNNN-NNNN.","path":"buyer.tin"}]', '[]')
)
INSERT INTO invoices (
    tenant_id, entity_id, invoice_number, status, issue_date, supplier_tin, supplier_name,
    buyer_tin, buyer_name, currency, subtotal, vat, total, violations, rule_set_version_id, rejection_reasons,
    irn, csid, qr_payload, created_at
)
SELECT
    '11111111-1111-1111-1111-111111111111', e.id, s.invoice_number, s.status, s.issue_date::date,
    s.supplier_tin, s.supplier_name, s.buyer_tin, s.buyer_name, 'NGN',
    s.subtotal::numeric, s.vat::numeric, s.total::numeric, s.violations::jsonb,
    CASE WHEN s.validated THEN rsv.id ELSE NULL END, s.rejection_reasons::jsonb,
    -- Only `accepted` rows carry these: a non-NULL irn is the "already cleared" sentinel
    -- (internal/invoice/submission_port.go), so a stray one makes a row unsubmittable.
    CASE WHEN s.status = 'accepted' THEN f.irn END,
    CASE WHEN s.status = 'accepted' THEN f.csid END,
    -- format(), not json[b]_build_object: jsonb reorders keys by length and json spaces
    -- around ':' -- only this reproduces mockQR's compact {irn,csid,tin,amt,cur}. The tin
    -- is the SUPPLIER's; the buyer TIN is the mock's trigger channel.
    CASE WHEN s.status = 'accepted' THEN
      translate(encode(convert_to(
        format('{"irn":"%s","csid":"%s","tin":"%s","amt":"%s","cur":"%s"}',
               f.irn, f.csid, s.supplier_tin, s.total::numeric(14,2), 'NGN'),
        'UTF8'), 'base64'), E'+/=\n', '-_')
    END,
    -- Explicit per-row offset, not a bare now(): the whole file runs in one implicit
    -- transaction, so now() is identical for every row and the register's created_at DESC
    -- would tie-break on a random uuid. Ceiling: keep each tenant under ~47 curated rows,
    -- or a mid-run re-seed re-anchors above enough e2e rows to rot the page-1 specs.
    now() - make_interval(secs => row_number() OVER (ORDER BY s.issue_date::date DESC, s.invoice_number DESC))
FROM invoice_seed s
JOIN business_entities e
  ON e.tenant_id = '11111111-1111-1111-1111-111111111111' AND e.tin = s.tin
CROSS JOIN (SELECT id FROM rule_set_versions WHERE is_active) rsv
-- Derived here, once, so qr_payload's embedded irn/csid cannot drift from the columns --
-- nothing in the schema correlates them. translate() strips base64 padding AND the newline
-- encode() inserts every 76 characters, giving the repo's base64url (RawURLEncoding) shape.
CROSS JOIN LATERAL (SELECT
    s.invoice_number || '-FBMOCK01-' || to_char(s.issue_date::date, 'YYYYMMDD') AS irn,
    translate(encode(sha256(convert_to(s.invoice_number, 'UTF8')), 'base64'), E'+/=\n', '-_') AS csid
) f
ON CONFLICT (tenant_id, entity_id, invoice_number) DO UPDATE SET
    status              = EXCLUDED.status,
    issue_date          = EXCLUDED.issue_date,
    supplier_tin        = EXCLUDED.supplier_tin,
    supplier_name       = EXCLUDED.supplier_name,
    buyer_tin           = EXCLUDED.buyer_tin,
    buyer_name          = EXCLUDED.buyer_name,
    currency            = EXCLUDED.currency,
    subtotal            = EXCLUDED.subtotal,
    vat                 = EXCLUDED.vat,
    total               = EXCLUDED.total,
    violations          = EXCLUDED.violations,
    rule_set_version_id = EXCLUDED.rule_set_version_id,
    rejection_reasons   = EXCLUDED.rejection_reasons,
    irn                 = EXCLUDED.irn,
    csid                = EXCLUDED.csid,
    qr_payload          = EXCLUDED.qr_payload,
    created_at          = EXCLUDED.created_at;

-- The in-house tenant's own portfolio. Honeywell owns exactly ONE entity
-- ([in-house-single-entity]) and had zero invoices, so Overview, Invoices, Approvals and
-- Reports all rendered an empty state on a fresh in-house sign-in. A SEPARATE block, not
-- more rows in invoice_seed above: that CTE carries no tenant column and its INSERT
-- hardcodes the firm's uuid. The fiscal derivation is copied from it verbatim -- without it
-- every accepted row here trips reconciliation's accepted_without_irn on cmd/reconciliation's
-- live 5-minute sweep. supplier_tin/supplier_name come off the entity row itself: a company
-- filing for itself IS its own supplier.
--
-- DEMO-2026-70## carry the mock adapter's RESERVED BUYER tins -- the buyer tin is its trigger
-- channel (mock_adapter.go's mockTriggerFor, exact match), and the invoice number's last digit
-- mirrors the tin's so a row and the outcome that produced it read as one. Each status is the
-- only terminal one its own trigger converges on: -0004 (unavailable) and -0006 (timeout)
-- return Retryable on EVERY attempt and exhaust to dead_lettered at MaxAttempts=8, so seeding
-- either as `accepted` would claim an outcome this sandbox cannot produce. -0005 is skipped --
-- it accepts exactly like -0001 and would add a row but no new outcome. DEMO-2026-80## are
-- ordinary counterparty invoices, so the portfolio is not made up entirely of triggers.
-- DEMO-2026-90## are submittable twins of the accept/reject/deferred triggers, left
-- `validated` with no seeded job or evidence: they have never been submitted.
WITH inhouse_invoice_seed (
    invoice_number, status, issue_date, buyer_tin, buyer_name,
    subtotal, vat, total, validated, violations, rejection_reasons
) AS (
  VALUES
    ('DEMO-2026-7001', 'accepted',  '2026-06-03', '99999999-0001', 'Sandbox APP (always accepts)',                400000.00, 30000.00, 430000.00, true, '[]', '[]'),
    ('DEMO-2026-7002', 'rejected',  '2026-06-05', '99999999-0002', 'Sandbox APP (always rejects)',                180000.00, 13500.00, 193500.00, true,
      '[]', '[{"code":"NGE-4102","message":"Customer tax identifier is not registered with the tax authority.","path":"buyer.tin"}]'),
    ('DEMO-2026-7003', 'accepted',  '2026-06-09', '99999999-0003', 'Sandbox APP (defers, then accepts)',          260000.00, 19500.00, 279500.00, true, '[]', '[]'),
    ('DEMO-2026-7004', 'failed',    '2026-06-12', '99999999-0004', 'Sandbox APP (unavailable, never verdicts)',   145000.00, 10875.00, 155875.00, true, '[]', '[]'),
    ('DEMO-2026-7006', 'failed',    '2026-06-16', '99999999-0006', 'Sandbox APP (times out, never verdicts)',      92000.00,  6900.00,  98900.00, true, '[]', '[]'),

    ('DEMO-2026-8001', 'accepted',  '2026-06-04', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)',    320000.00, 24000.00, 344000.00, true, '[]', '[]'),
    ('DEMO-2026-8002', 'accepted',  '2026-06-11', '20011122-0001', 'Zenith Freight & Logistics Ltd (accepted by the authority)',    215000.00, 16125.00, 231125.00, true, '[]', '[]'),
    ('DEMO-2026-8003', 'validated', '2026-06-18', '20022233-0002', 'Lagos Textiles Mart (validated, ready to submit)',               88000.00,  6600.00,  94600.00, true, '[]', '[]'),
    ('DEMO-2026-8004', 'draft',     '2026-06-22', '20044455-0004', 'Ibadan Consumer Goods Ltd (draft, not yet validated)',           64000.00,  4800.00,  68800.00, false, '[]', '[]'),
    ('DEMO-2026-8005', 'draft',     '2026-06-25', '20055566-0005', 'Port Harcourt Marine Supplies Ltd (VAT off the standard rate)',  56000.00,  5600.00,  61600.00, true,
      '[{"rule_key":"vat-standard-rate","severity":"error","message":"VAT must equal 7.5% of the subtotal."}]', '[]'),

    ('DEMO-2026-9001', 'validated', '2026-06-26', '99999999-0001', 'Sandbox APP (always accepts)',       240000.00, 18000.00, 258000.00, true, '[]', '[]'),
    ('DEMO-2026-9002', 'validated', '2026-06-27', '99999999-0002', 'Sandbox APP (always rejects)',       168000.00, 12600.00, 180600.00, true, '[]', '[]'),
    ('DEMO-2026-9003', 'validated', '2026-06-28', '99999999-0003', 'Sandbox APP (defers, then accepts)', 196000.00, 14700.00, 210700.00, true, '[]', '[]')
)
INSERT INTO invoices (
    tenant_id, entity_id, invoice_number, status, issue_date, supplier_tin, supplier_name,
    buyer_tin, buyer_name, currency, subtotal, vat, total, violations, rule_set_version_id, rejection_reasons,
    irn, csid, qr_payload, created_at
)
SELECT
    '22222222-2222-2222-2222-222222222222', e.id, s.invoice_number, s.status, s.issue_date::date,
    e.tin, e.name, s.buyer_tin, s.buyer_name, 'NGN',
    s.subtotal::numeric, s.vat::numeric, s.total::numeric, s.violations::jsonb,
    CASE WHEN s.validated THEN rsv.id ELSE NULL END, s.rejection_reasons::jsonb,
    CASE WHEN s.status = 'accepted' THEN f.irn END,
    CASE WHEN s.status = 'accepted' THEN f.csid END,
    CASE WHEN s.status = 'accepted' THEN
      translate(encode(convert_to(
        format('{"irn":"%s","csid":"%s","tin":"%s","amt":"%s","cur":"%s"}',
               f.irn, f.csid, e.tin, s.total::numeric(14,2), 'NGN'),
        'UTF8'), 'base64'), E'+/=\n', '-_')
    END,
    -- Explicit per-row offset, same reason and same ceiling as the firm block above.
    now() - make_interval(secs => row_number() OVER (ORDER BY s.issue_date::date DESC, s.invoice_number DESC))
FROM inhouse_invoice_seed s
JOIN business_entities e
  ON e.tenant_id = '22222222-2222-2222-2222-222222222222' AND e.tin = '20665510-0001'
CROSS JOIN (SELECT id FROM rule_set_versions WHERE is_active) rsv
CROSS JOIN LATERAL (SELECT
    s.invoice_number || '-FBMOCK01-' || to_char(s.issue_date::date, 'YYYYMMDD') AS irn,
    translate(encode(sha256(convert_to(s.invoice_number, 'UTF8')), 'base64'), E'+/=\n', '-_') AS csid
) f
ON CONFLICT (tenant_id, entity_id, invoice_number) DO UPDATE SET
    status              = EXCLUDED.status,
    issue_date          = EXCLUDED.issue_date,
    supplier_tin        = EXCLUDED.supplier_tin,
    supplier_name       = EXCLUDED.supplier_name,
    buyer_tin           = EXCLUDED.buyer_tin,
    buyer_name          = EXCLUDED.buyer_name,
    currency            = EXCLUDED.currency,
    subtotal            = EXCLUDED.subtotal,
    vat                 = EXCLUDED.vat,
    total               = EXCLUDED.total,
    violations          = EXCLUDED.violations,
    rule_set_version_id = EXCLUDED.rule_set_version_id,
    rejection_reasons   = EXCLUDED.rejection_reasons,
    irn                 = EXCLUDED.irn,
    csid                = EXCLUDED.csid,
    qr_payload          = EXCLUDED.qr_payload,
    created_at          = EXCLUDED.created_at;

-- Line items for the invoices above. Conflict target is line_items_invoice_line_no_uq
-- (invoice_id, line_no); invoice_id is resolved by joining back on invoice_number, which
-- (see above) is globally unique within this tenant's DEMO-2026-#### range, so the join
-- is unambiguous with no entity_id needed on this side. Quantity * unit_price = line_total
-- = subtotal for every invoice EXCEPT DEMO-2026-4002 (Balogun), whose single line
-- deliberately sums to 90000.00 against a stated subtotal of 100000.00 -- the
-- line-items-sum-subtotal violation seeded above.
WITH line_item_seed (invoice_number, line_no, description, quantity, unit_price, line_total) AS (
  VALUES
    ('DEMO-2026-1001', 1, 'Consulting services - June retainer',       1,  300000.00, 300000.00),
    ('DEMO-2026-1001', 2, 'Implementation support - onsite',           1,  200000.00, 200000.00),
    ('DEMO-2026-1002', 1, 'Fabric rolls - premium cotton',             10, 22000.00,  220000.00),
    ('DEMO-2026-1003', 1, 'Office equipment supply',                   4,  45000.00,  180000.00),
    ('DEMO-2026-1004', 1, 'Agro-chemical supply batch',                1,  95000.00,  95000.00),
    ('DEMO-2026-1005', 1, 'Textile finishing service',                 3,  20000.00,  60000.00),
    ('DEMO-2026-1006', 1, 'Warehouse rental - June',                   1,  75000.00,  75000.00),
    ('DEMO-2026-1007', 1, 'Office furniture consignment',              3,  40000.00,  120000.00),
    ('DEMO-2026-1008', 1, 'Packaging materials - bulk',                2,  48000.00,  96000.00),
    ('DEMO-2026-1009', 1, 'Warehouse racking system',                  4,  36000.00,  144000.00),

    ('DEMO-2026-2001', 1, 'Generator maintenance contract - Q2',       1,  200000.00, 200000.00),
    ('DEMO-2026-2001', 2, 'Replacement parts - filters & belts',       2,  55000.00,  110000.00),
    ('DEMO-2026-2002', 1, 'Packaged consumer goods - assorted',        8,  16000.00,  128000.00),
    ('DEMO-2026-2003', 1, 'Freight haulage - Lagos-Kano route',        1,  84000.00,  84000.00),
    ('DEMO-2026-2004', 1, 'Spare parts order',                         5,  9000.00,   45000.00),
    ('DEMO-2026-2005', 1, 'Bulk beverage crates',                      1,  66000.00,  66000.00),

    ('DEMO-2026-3001', 1, 'Textile bulk order - cotton blend',         2,  100000.00, 200000.00),
    ('DEMO-2026-3001', 2, 'Express delivery surcharge',                1,  65000.00,  65000.00),
    ('DEMO-2026-3002', 1, 'Woven fabric supply',                       1,  142000.00, 142000.00),
    ('DEMO-2026-3003', 1, 'Uniform batch - retail contract',           1,  88000.00,  88000.00),
    ('DEMO-2026-3004', 1, 'Dye & finishing chemicals',                 1,  51000.00,  51000.00),
    ('DEMO-2026-3005', 1, 'Sample swatches order',                     1,  39000.00,  39000.00),

    ('DEMO-2026-4001', 1, 'Fertiliser - 50kg bags',                    4,  33000.00,  132000.00),
    ('DEMO-2026-4001', 2, 'Seed stock - hybrid maize',                 2,  33000.00,  66000.00),
    ('DEMO-2026-4002', 1, 'Poultry feed - bulk order',                 9,  10000.00,  90000.00),
    ('DEMO-2026-4003', 1, 'Irrigation pump unit',                      2,  31000.00,  62000.00),
    ('DEMO-2026-4004', 1, 'Crop storage sacks',                        4,  10000.00,  40000.00),

    ('DEMO-2026-5001', 1, 'Pharmaceutical supply - antibiotics batch', 3,  29000.00,  87000.00),
    ('DEMO-2026-5001', 2, 'Cold-chain packaging',                      2,  29000.00,  58000.00),
    ('DEMO-2026-5002', 1, 'Vaccine cold storage order',                3,  70000.00,  210000.00),
    ('DEMO-2026-5003', 1, 'Generic medicine restock',                  4,  19000.00,  76000.00),
    ('DEMO-2026-5004', 1, 'Medical consumables order',                 3,  11000.00,  33000.00),

    ('DEMO-2026-6001', 1, 'Freight consignment - Lagos-Abuja',         1,  72000.00,  72000.00),
    ('DEMO-2026-6002', 1, 'Container handling fee',                    1,  54000.00,  54000.00),
    ('DEMO-2026-6003', 1, 'Warehousing - monthly',                     1,  47000.00,  47000.00),

    -- Honeywell (in-house). The 1xxx-6xxx and 7xxx/8xxx/9xxx number ranges are disjoint by
    -- construction, which is what keeps the invoice_number join below unambiguous once it
    -- spans two tenants.
    ('DEMO-2026-7001', 1, 'Industrial gas cylinder supply',            2,  200000.00, 400000.00),
    ('DEMO-2026-7002', 1, 'Packaging film order',                      1,  180000.00, 180000.00),
    ('DEMO-2026-7003', 1, 'Compressor unit',                           1,  180000.00, 180000.00),
    ('DEMO-2026-7003', 2, 'Installation & commissioning',              1,  80000.00,  80000.00),
    ('DEMO-2026-7004', 1, 'Boiler spares consignment',                 1,  145000.00, 145000.00),
    ('DEMO-2026-7006', 1, 'Preventive maintenance retainer',           1,  92000.00,  92000.00),

    ('DEMO-2026-8001', 1, 'Steel coil supply',                         4,  60000.00,  240000.00),
    ('DEMO-2026-8001', 2, 'Haulage surcharge',                         1,  80000.00,  80000.00),
    ('DEMO-2026-8002', 1, 'Grain dryer components',                    5,  43000.00,  215000.00),
    ('DEMO-2026-8003', 1, 'Conveyor belt replacement',                 1,  88000.00,  88000.00),
    ('DEMO-2026-8004', 1, 'Textile machinery parts',                   4,  16000.00,  64000.00),
    ('DEMO-2026-8005', 1, 'Quarterly service contract',                1,  56000.00,  56000.00),
    ('DEMO-2026-9001', 1, 'Process valve assembly',                    2,  120000.00, 240000.00),
    ('DEMO-2026-9002', 1, 'Conveyor motor unit',                       1,  168000.00, 168000.00),
    ('DEMO-2026-9003', 1, 'Heat exchanger overhaul',                   4,  49000.00,  196000.00)
)
INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total)
SELECT i.tenant_id, i.id, li.line_no, li.description,
       li.quantity::numeric, li.unit_price::numeric, li.line_total::numeric
FROM line_item_seed li
JOIN invoices i
  ON i.tenant_id IN ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222')
 AND i.invoice_number = li.invoice_number
ON CONFLICT (invoice_id, line_no) DO UPDATE SET
    description = EXCLUDED.description,
    quantity    = EXCLUDED.quantity,
    unit_price  = EXCLUDED.unit_price,
    line_total  = EXCLUDED.line_total;

-- A plausible invoice_status_history trail per invoice above -- InvoiceDetail's own
-- "Status history" panel renders for every live invoice, so one sitting at 'accepted'
-- with literally zero history rows (impossible via the real Store.Create ->
-- Gate.Validate -> transitions path) would look broken the moment a demo/dev user
-- clicks into it. `chains` encodes the exact (from_status, to_status) sequence each
-- final status implies -- verified against the real transition edges
-- e2e/topology/invoice-surfaces.spec.ts's own live round trips observe (a blocked
-- `draft` validate call writes NO row; every other edge writes exactly one). `changed_at`
-- is an explicit, increasing offset from a fixed anchor, never `now()` -- every
-- statement in this file runs inside ONE implicit transaction (bootstrap.go's Seed calls
-- conn.Exec once on the whole file's text), so `now()` is IDENTICAL for every row this
-- script inserts, and History's own `ORDER BY changed_at ASC, id ASC`
-- (internal/invoice/store.go) would then tie-break on a random gen_random_uuid(),
-- scrambling the chain's visible order.
--
-- No unique constraint exists on this table to key an ON CONFLICT off (append-only by
-- GRANT, not by a DB constraint -- migrations/20260714111246_invoice_status_history.sql's
-- header) -- idempotency is instead a NOT EXISTS guard on the (invoice_id, from_status,
-- to_status) triple, scoped to this seed's own DEMO-2026-#### invoices only, so a re-run
-- never duplicates a row this script already inserted and never touches any other
-- invoice's real history.
WITH chains (status, ord, from_status, to_status) AS (
  VALUES
    ('draft',     1, NULL,        'draft'),
    ('validated', 1, NULL,        'draft'),
    ('validated', 2, 'draft',     'validated'),
    ('queued',    1, NULL,        'draft'),
    ('queued',    2, 'draft',     'validated'),
    ('queued',    3, 'validated', 'queued'),
    ('submitted', 1, NULL,        'draft'),
    ('submitted', 2, 'draft',     'validated'),
    ('submitted', 3, 'validated', 'queued'),
    ('submitted', 4, 'queued',    'submitted'),
    ('accepted',  1, NULL,        'draft'),
    ('accepted',  2, 'draft',     'validated'),
    ('accepted',  3, 'validated', 'queued'),
    ('accepted',  4, 'queued',    'submitted'),
    ('accepted',  5, 'submitted', 'accepted'),
    ('rejected',  1, NULL,        'draft'),
    ('rejected',  2, 'draft',     'validated'),
    ('rejected',  3, 'validated', 'queued'),
    ('rejected',  4, 'queued',    'rejected'),
    ('failed',    1, NULL,        'draft'),
    ('failed',    2, 'draft',     'validated'),
    ('failed',    3, 'validated', 'queued'),
    -- ord 5, not 4: on an environment seeded before this row was terminal, the guard below
    -- leaves the old `queued -> submitted` row (ord 4) in place, and a tie on changed_at
    -- would let the panel render `submitted` as the LAST step of a failed invoice.
    ('failed',    5, 'queued',    'failed')
)
INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor, changed_at)
SELECT
    i.tenant_id, i.id, c.from_status, c.to_status, 'system',
    '2026-06-01 08:00:00+00'::timestamptz + make_interval(mins => c.ord * 15)
FROM invoices i
JOIN chains c ON c.status = i.status
WHERE i.tenant_id IN ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222')
  AND i.invoice_number LIKE 'DEMO-2026-%'
  AND NOT EXISTS (
    SELECT 1 FROM invoice_status_history h
     WHERE h.invoice_id = i.id
       AND h.to_status = c.to_status
       AND h.from_status IS NOT DISTINCT FROM c.from_status
  );

-- The submission record behind each outcome-coverage invoice above. The column is `state`,
-- never `status`: it shares four names with invoices.status and joins to none of them.
--
-- TERMINAL states ONLY (accepted / rejected / failed / dead_lettered). A seeded `pending`
-- job trips reconciliation's pending_too_long 24h after this environment last booted and a
-- `submitting` one trips submitting_orphan after 15 minutes -- on a real 5-minute sweep, so
-- it would file a drift audit every cycle forever. Live in-flight behaviour stays
-- demonstrable by submitting a NEW invoice against a reserved TIN; the worker and the mock
-- adapter are both running on this deployment.
--
-- attempts is the real retry budget, and polls continue the submit's own numbering: 1 for a
-- first-attempt verdict; 3 for the pending row, because its first Ref carries n=2 and Poll
-- consumes one per call, so it takes submit + TWO polls to converge; 8 for the two that
-- exhausted the budget, dead-lettered on the 8th execution (job.Attempt >= MaxAttempts).
-- created_at/updated_at take their now() defaults -- updated_at's trigger is BEFORE UPDATE
-- only, so setting it on INSERT would be ignored anyway.
WITH job_seed (invoice_number, state, attempts, last_error) AS (
  VALUES
    ('DEMO-2026-7001', 'accepted',      1, NULL),
    ('DEMO-2026-7002', 'rejected',      1, NULL),
    ('DEMO-2026-7003', 'accepted',      3, NULL),
    ('DEMO-2026-7004', 'dead_lettered', 8, 'submission: mock APP is temporarily unavailable'),
    ('DEMO-2026-7006', 'dead_lettered', 8, 'submission: mock APP timed out in flight')
)
INSERT INTO submission_jobs (
    tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state, attempts, last_error
)
SELECT i.tenant_id, i.id, 'demo-seed:' || i.invoice_number, 'mock', 'v1',
       j.state, j.attempts, j.last_error
FROM job_seed j
JOIN invoices i
  ON i.tenant_id = '22222222-2222-2222-2222-222222222222' AND i.invoice_number = j.invoice_number
ON CONFLICT (tenant_id, idempotency_key) DO UPDATE SET
    state      = EXCLUDED.state,
    attempts   = EXCLUDED.attempts,
    last_error = EXCLUDED.last_error;

-- One app_exchange row per ATTEMPT against the APP, which is what makes the five outcomes
-- distinguishable: invoices.status collapses -0004 and -0006 onto `failed`, and only the
-- http_status here tells a 503 exhaustion from a timeout that never got a response at all.
-- `outcome` is a TRANSPORT outcome, not a verdict -- every one of these reached the wire,
-- so all of them are 'sent'.
--
-- Bodies stay NULL: these rows are seeded evidence, and a synthesized request/response body
-- would be a fabricated wire capture rather than a recorded one. Idempotency is a NOT EXISTS
-- guard on (submission_job_id, operation, attempt) -- the table has no unique constraint to
-- key an ON CONFLICT off, and is append-only by grant.
--
-- The join is pinned to the seed's OWN job by idempotency_key: a real submit adds a second job
-- for the same invoice (ensureSubmissionJob runs before every gate in worker.go), and an
-- invoice_id-only join would re-attribute this synthetic evidence to it on the next re-seed.
WITH exchange_seed (invoice_number, operation, attempt_from, attempt_to, http_status, latency_ms) AS (
  VALUES
    ('DEMO-2026-7001', 'submit', 1, 1, 200,  142),
    ('DEMO-2026-7002', 'submit', 1, 1, 422,  158),
    ('DEMO-2026-7003', 'submit', 1, 1, 202,  131),
    ('DEMO-2026-7003', 'poll',   2, 2, 202,  118),
    ('DEMO-2026-7003', 'poll',   3, 3, 200,  124),
    ('DEMO-2026-7004', 'submit', 1, 8, 503,   96),
    ('DEMO-2026-7006', 'submit', 1, 8, NULL, 30000)
)
INSERT INTO app_exchange (
    tenant_id, submission_job_id, invoice_id, operation, outcome, attempt,
    http_status, latency_ms, adapter, adapter_version
)
SELECT j.tenant_id, j.id, j.invoice_id, x.operation, 'sent', a.attempt,
       x.http_status, x.latency_ms, 'mock', 'v1'
FROM exchange_seed x
JOIN invoices i
  ON i.tenant_id = '22222222-2222-2222-2222-222222222222' AND i.invoice_number = x.invoice_number
JOIN submission_jobs j
  ON j.tenant_id = i.tenant_id AND j.invoice_id = i.id
 AND j.idempotency_key = 'demo-seed:' || i.invoice_number
CROSS JOIN LATERAL generate_series(x.attempt_from, x.attempt_to) AS a(attempt)
WHERE NOT EXISTS (
    SELECT 1 FROM app_exchange e
     WHERE e.submission_job_id = j.id
       AND e.operation = x.operation
       AND e.attempt = a.attempt
  );

