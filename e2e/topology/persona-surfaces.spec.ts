// PERSONA-01-03 (Backlog task-272): the in-house app-surface sweep, the Approvals badge
// oracle, and the two persona-DIFFERENCE assertions (sidebar roster, entity scoping).
//
// Why this file exists: every functional browser spec in this package drove the FIRM
// persona. The in-house persona appeared in exactly two assertions in the whole suite
// (auth.spec.ts's identity-switch regression and import-wizard.spec.ts's
// [inhouse-can-start]), neither of which navigates past the surface it lands on -- so an
// in-house-only surface could break in production without a single test going red. That is
// this story's defect class, and these four tests are the guard against it.
//
// Fixtures are created through e2e/api/client.ts BEFORE the browser is driven, never
// through the UI: [write-path-deferred] fences the in-house WRITE path (import filing /
// manual-entry persistence) out of this story, and the server enforces it anyway (the
// commit gate needs an entity_id, and inhouseClient() pins active.entityId to null --
// lib/clients.ts). The same "own entity per test" discipline as invoice-surfaces.spec.ts
// and import-wizard.spec.ts: this suite runs serially (fullyParallel:false, workers:1,
// playwright.topology.config.ts) against the same shared, never-reset dev DB.
//
// COUNT ASSERTIONS: tenant B accumulates invoices forever on that DB, so no assertion here
// may name a literal count. Only two shapes are legal, and both are used below --
// (1) compared against a LIVE API read taken in the same test (the Approvals badge, the
// Overview KPI), and (2) containment of rows this test itself created. A hardcoded '3'
// would pass once and rot on the next run.
import { test, expect, type Page } from '@playwright/test'
import { createEntity, createInvoice, login, rollup, validateInvoice, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { collectErrors, sidebarRoster, signInAs } from '../personaSession'
import { FIRM_PERSONA, INHOUSE_PERSONA, VALIDATION_EXPECTED } from './targets'

// cleanInvoiceFields(): a local copy of invoice-surfaces.spec.ts:127-141 (that file exports
// nothing). The FLAT wire shape POST /v1/invoices takes -- supplier_tin/vat as strings --
// NOT api/fixtures.ts's nested /v1/validate envelope; internal/invoice/payload.go's
// MBSPayload re-nests them server-side. Fires ZERO violations against the active v2 rule
// set: a canonical Luhn-valid supplier TIN, VAT at exactly 7.5% of the subtotal, and one
// line item that reconciles to it -- which is what promotes draft -> validated on
// POST .../validate.
function cleanInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// createValidatedInvoice(): create + validate, with the fixture guard the whole Approvals
// oracle rests on. POST .../validate is THE gate that earns `validated` (the transitions
// endpoint 409s on that target, internal/invoice/handlers.go), and a zero-violation invoice
// auto-promotes inside it. A blocking violation comes back as a 200 carrying violations as
// DATA, never an HTTP error -- so without this assertion a rule-set change would silently
// degrade the fixture to `draft` and Test 2 would fail much later with an unreadable
// message about a badge that never rendered.
async function createValidatedInvoice(token: string, entityId: string, invoiceNumber: string): Promise<void> {
  const created = await createInvoice(token, { entity_id: entityId, ...cleanInvoiceFields(invoiceNumber) })
  const validated = await validateInvoice(token, created.id)
  expect(
    validated.status,
    `the clean fixture must promote draft->validated; if this fails the rule set moved under cleanInvoiceFields()`,
  ).toBe('validated')
}

function sidebar(page: Page) {
  return page.locator('aside.pf-sidebar')
}

// navButton(): a sidebar nav item by its rendered label. Scoped to the aside so it can
// never pick up a same-named control elsewhere on the screen (the header's lowercase "New
// invoice" CTA, a view's own buttons). Playwright matches `name` as a case-insensitive
// SUBSTRING by default, which is what makes this work on a badged item too: the Approvals
// button's accessible name is "Approvals <count>" once its badge renders.
function navButton(page: Page, label: string) {
  return sidebar(page).getByRole('button', { name: label })
}

async function goTo(page: Page, label: string): Promise<void> {
  await navButton(page, label).click()
}

// invoiceRowByNumber(): filter({ has }) with an EXACT text match, not { hasText } -- every
// fixture number below carries the same Date.now() stamp, so a substring filter could match
// two rows whose numbers share a prefix.
function invoiceRowByNumber(page: Page, invoiceNumber: string) {
  return page.getByTestId('invoice-row').filter({ has: page.getByText(invoiceNumber, { exact: true }) })
}

// selectEntity(): a fourth copy of invoice-surfaces.spec.ts / import-wizard.spec.ts's
// helper of the same name -- this package's convention for small Page-driving helpers (see
// invoice-surfaces.spec.ts:31-34's own note on why the copies are not hoisted). Required by
// the FIRM half of Test 4 only: [dashboard-scope-per-client] made Invoices a CLIENT-scoped
// surface, and signing in leaves the switcher on whatever `clients[0]` resolves to
// (portfolio's ORDER BY name ASC), never this test's own stamped entity.
//
// Known latent limit, shared with both existing copies and NOT introduced here: the
// dropdown is position:absolute inside .pf-shell (height:100vh, overflow:hidden) with no
// max-height (Sidebar.tsx:158), so once a firm tenant owns more entities than fit the
// viewport, the option for a late-sorting name is unreachable and this click fails with
// "outside of the viewport". Bounded today by per-PR ephemeral environments; a product fix,
// not a test one.
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// The two rosters, derived from Sidebar.tsx:115-127 (navGroups) x glyphs.tsx:62-110 (each
// NavDef's label). Firm mode splits into a CLIENT group of 6 and a FIRM-WIDE group of 3;
// sidebarRoster() flattens both in DOM order, which is the right shape for the claim being
// made -- the grouping is presentation, the ROSTER is which surfaces this persona has.
const FIRM_ROSTER = ['Overview', 'Invoices', 'Validation', 'Rules', 'Customers', 'Reports', 'Workflows', 'Clients', 'Settings']
const INHOUSE_ROSTER = ['Overview', 'Invoices', 'Validation', 'Workflows', 'Rules', 'Approvals', 'Reports', 'Settings']

// ---------------------------------------------------------------------------------------
// Test 1 -- the in-house sidebar sweep (Core AC 2, AC 8)
// ---------------------------------------------------------------------------------------
test('in-house sweep: every sidebar surface renders real content for the in-house persona', async ({ page }) => {
  // Seven surfaces, each with its own live round trip, on a possibly cold fleet. The
  // existing precedent for the headroom is in-file, never a widened global timeout
  // (invoice-surfaces.spec.ts:470/:623/:793 all take 120s the same way).
  test.setTimeout(120_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.B)
  const stamp = Date.now()
  // Two entities, so the list assertion below also exercises the tenant-wide (un-scoped)
  // in-house fetch. Each gets its own freshTin() -- the dev DB is never reset and
  // business_entities has a per-tenant unique TIN index.
  const entityA = await createEntity(token, { name: `PERSONA-01 sweep A ${stamp}`, tin: freshTin() })
  const entityB = await createEntity(token, { name: `PERSONA-01 sweep B ${stamp}`, tin: freshTin() })
  const validatedNumber = `INV-P01-SWEEP-V-${stamp}`
  const draftNumber = `INV-P01-SWEEP-D-${stamp}`
  await createValidatedInvoice(token, entityA.id, validatedNumber)
  await createInvoice(token, { entity_id: entityB.id, ...cleanInvoiceFields(draftNumber) })

  // Every fixture write is complete BEFORE sign-in: DashboardActive and Sidebar each fetch
  // the rollup once at mount (useAsync, `immediate`, no deps), so the numbers on screen are
  // a page-load snapshot. A write landing after this line would never show up.
  await signInAs(page, 'inhouse')

  // --- Overview -------------------------------------------------------------------------
  // The h1 is the TENANT NAME, not the word "Overview" (DashboardActive.tsx:73-75 renders
  // ctx.user.tenantName in in-house mode) -- which is also the strongest available proof
  // that the in-house workspace is what drew, and why INHOUSE_PERSONA is the right source
  // for it rather than a bare string literal.
  await expect(page.getByRole('heading', { level: 1, name: INHOUSE_PERSONA.tenantName, exact: true })).toBeVisible()
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()
  await expect(page.getByText('Firm-wide invoice compliance', { exact: true })).toBeVisible()

  // The live-read oracle for the Invoices KPI. Read AFTER sign-in with no writes in
  // between, so the browser's own rollup fetch and this one see the same DB state; the
  // suite is serial, so nothing else can write to tenant B in the window. scopedBucket()
  // returns rollup.totals unchanged for in-house (lib/dashboard.ts:213-214), and the tile
  // value is the sum over all seven status counts (DashboardActive.tsx:101).
  const live = await rollup(token)
  const invoiceTotal = Object.values(live.totals.counts).reduce((a, b) => a + b, 0)
  expect(invoiceTotal, 'the fixtures above must leave this tenant with invoices to count').toBeGreaterThan(0)
  // No data-testid on this dashboard ([no-testids-on-portfolio-dashboard]); `.pf-dash-row-a
  // > .pf-grid-2` is the KPI grid and is unique on the screen. The title match is an
  // ANCHORED REGEX on purpose: hasText with a plain string is a case-insensitive substring,
  // so 'Invoices' would also match the "Failing invoices" tile and blow strict mode.
  const invoicesKpiValue = page
    .locator('.pf-dash-row-a .pf-grid-2 > div')
    .filter({ has: page.locator('.card-title', { hasText: /^Invoices$/ }) })
    .locator('span.money')
  await expect(invoicesKpiValue).toHaveText(String(invoiceTotal))

  // --- Invoices -------------------------------------------------------------------------
  await goTo(page, 'Invoices')
  await expect(page.getByTestId('invoices-list')).toBeVisible()
  const validatedRow = invoiceRowByNumber(page, validatedNumber)
  await expect(validatedRow).toBeVisible()
  await expect(validatedRow.getByTestId('invoice-status-badge')).toContainText('VALIDATED')
  await expect(invoiceRowByNumber(page, draftNumber)).toBeVisible()

  // --- Validation -----------------------------------------------------------------------
  // The playground round trip, driven exactly as validation.spec.ts:53-70 drives it as the
  // FIRM persona. ValidationView reads neither ctx.active nor an entity id, so this is
  // persona-agnostic by construction -- which is the point: it proves the in-house persona
  // reaches the same live engine, tagged with the same rule-set version.
  await goTo(page, 'Validation')
  await page.getByRole('button', { name: /Has violations/ }).click()
  await page.getByRole('button', { name: 'Validate' }).click()
  const violations = page.getByRole('table')
  await expect(violations).toBeVisible()
  for (const key of VALIDATION_EXPECTED.sampleRuleKeys) {
    await expect(violations).toContainText(key)
  }
  await expect(violations.locator('tbody tr').first().locator('td').last()).toHaveText(String(VALIDATION_EXPECTED.ruleSetVersion))

  // --- Workflows ------------------------------------------------------------------------
  // Pins MOCK FIXTURE BEHAVIOUR (SEED_INHOUSE_POLICIES, lib/workflows.ts:167-186), not a
  // backend contract -- there is no approvals endpoint; ctx.savePolicy lives in App.tsx
  // useState. [workflows-included]'s recorded trade-off: sweeping it is still worth doing,
  // because a persona-conditional render breaking is exactly what this story guards.
  // Swept here; its COVERAGE CELL belongs to [PERSONA-01-04].
  await goTo(page, 'Workflows')
  await expect(page.getByRole('heading', { level: 1, name: 'Approval policies', exact: true })).toBeVisible()
  // active.short === the tenant name here: shortName()'s LEGAL_SUFFIX (/\s+(ltd\.?|limited|
  // plc)$/i, lib/clients.ts) does not strip "Group".
  await expect(
    page.getByText(`Who must sign off before ${INHOUSE_PERSONA.tenantName} transmits an invoice.`, { exact: true }),
  ).toBeVisible()
  await expect(page.getByText('Company approval policy', { exact: true })).toBeVisible()
  await expect(page.getByText('Capital expenditure', { exact: true })).toBeVisible()

  // --- Rules ----------------------------------------------------------------------------
  // Pins MOCK FIXTURE BEHAVIOUR (GOLDEN_RULES / GOLDEN_SET, lib/rules.ts), not a backend
  // contract -- the golden ruleset rendered here is static app data, unrelated to the live
  // MBS rule set the Validation surface above round-trips. [workflows-included]'s trade-off.
  await goTo(page, 'Rules')
  await expect(page.getByRole('heading', { level: 1, name: 'Rules', exact: true })).toBeVisible()
  await expect(page.getByText('Golden ruleset · NG-MBS', { exact: false })).toBeVisible()
  await expect(page.getByText('INHERITED · GOLDEN RULESET', { exact: false })).toBeVisible()
  // Golden rules render a LOCK, never a disabled toggle (RulesView.tsx:230-233) -- at least
  // one row must actually be drawn, so this is a content assertion and not a bare mount.
  await expect(page.getByText('LOCKED', { exact: true }).first()).toBeVisible()

  // --- Reports --------------------------------------------------------------------------
  // Pins STATIC COPY plus the tenant identity, not the report body: ReportsView forks on
  // rows.length (an empty-state branch at :140), so asserting the body would make this
  // surface's result depend on fixture volume rather than on the surface rendering.
  // [workflows-included]'s trade-off.
  await goTo(page, 'Reports')
  // The source writes "Reports &amp; analytics"; the RENDERED text carries a literal '&'.
  await expect(page.getByRole('heading', { level: 1, name: 'Reports & analytics', exact: true })).toBeVisible()
  await expect(page.getByText('TAX REPORTING', { exact: true })).toBeVisible()
  await expect(page.getByText('tax summary, period to date', { exact: false })).toContainText(INHOUSE_PERSONA.tenantName)

  // --- Settings -------------------------------------------------------------------------
  // Pins STATIC COPY (SETTINGS_TABS, data.tsx:282-286), not a backend contract --
  // connectors/API keys/certificates are all mock. [workflows-included]'s trade-off.
  await goTo(page, 'Settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()
  await expect(page.getByText('WORKSPACE CONFIGURATION', { exact: true })).toBeVisible()
  await expect(page.getByText('Integrations, developer access, and signing certificates', { exact: true })).toBeVisible()
  for (const tab of ['ERP connectors', 'API & webhooks', 'Signing & certificates']) {
    await expect(page.getByRole('button', { name: tab, exact: true })).toBeVisible()
  }

  // The eighth in-house surface, Approvals, is swept by Test 2 below -- it needs its own
  // fixtures and its own live-read oracle, and folding it in here would bury the one
  // assertion this story's AC 3 actually turns on.

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 2 -- Approvals as an in-house-exclusive surface (Core AC 3)
// ---------------------------------------------------------------------------------------
test('Approvals: the in-house-only badge equals the live validated count, and the surface opens', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.B)
  const stamp = Date.now()
  const entity = await createEntity(token, { name: `PERSONA-01 approvals ${stamp}`, tin: freshTin() })
  // TWO validated fixtures, not one: with a single invoice a badge that renders a boolean
  // ("1" whenever anything is validated) would be indistinguishable from a real count.
  const validatedNumbers = [`INV-P01-APPR-1-${stamp}`, `INV-P01-APPR-2-${stamp}`]
  for (const number of validatedNumbers) {
    await createValidatedInvoice(token, entity.id, number)
  }

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())

  // --- Clause 1: LOAD-BEARING. The badge is asserted EQUAL to a live API read. ------------
  // Same endpoint, same field, same tenant on both sides: the Sidebar calls
  // getRollup -> GET /api/dashboard/v1/rollup (lib/dashboard.ts:81-82), scopedBucket()
  // returns rollup.totals for in-house (:213-214), and the badge is
  // bucket.counts.validated (Sidebar.tsx:91); rollup(token) below hits the identical route
  // and reads the identical field. The browser's in-house session mints a token for the
  // same subject/tenant as PERSONAS.B (frontend/app/src/auth.ts).
  const live = await rollup(token)
  const expectedValidated = live.totals.counts.validated
  // The badge is ABSENT, not "0", when the count is zero (Sidebar.tsx:91) -- so this guard
  // is what keeps the assertion below from being vacuous rather than mere defensiveness.
  expect(expectedValidated, 'the fixtures must leave >=1 validated invoice, else the badge never renders').toBeGreaterThan(0)
  // The badge span is the only className="mono" inside a nav button (Sidebar.tsx:240-244);
  // the glyph beside it is an inline <svg> carrying no text, so this cannot pick up the
  // label. Scoped to the aside; in-house renders no company switcher, so every in-scope
  // button is a nav button.
  const approvalsBadge = navButton(page, 'Approvals').locator('.mono')
  await expect(approvalsBadge).toHaveText(String(expectedValidated))

  // --- The click did something: the active-nav state moves to Approvals. ------------------
  // App.tsx:266's nav('approvals') sets view='invoices' + filter='Pending', and
  // Sidebar.tsx:128-129 turns that pair into activeNav='approvals'. That highlight is the
  // ONLY thing in the rendered tree that distinguishes Approvals from Invoices (see the
  // comment on clause 2 below), and it carries no class, aria-current or data attribute --
  // Sidebar.tsx:235 expresses it purely as inline style. font-weight is the discriminator
  // used here rather than the accent bar's colour, which resolves to a design-system token
  // whose serialized value is a moving target. Asserted as a TRANSITION (before -> after)
  // and PAIRED against Invoices, so "everything is bold" cannot pass it.
  const approvalsNav = navButton(page, 'Approvals')
  const invoicesNav = navButton(page, 'Invoices')
  await expect(approvalsNav, 'Approvals is not the active nav item before the click').toHaveCSS('font-weight', '500')
  await approvalsNav.click()
  await expect(approvalsNav, 'Approvals became the active nav item').toHaveCSS('font-weight', '600')
  await expect(invoicesNav, 'and Invoices did not').toHaveCSS('font-weight', '500')

  // WEAK CORROBORATOR — not a full-strength assertion, and deliberately so.
  // Approvals is not a separate screen: App.tsx:266's nav('approvals') sets
  // view='invoices' + filter='Pending', and ctx.filter's ONLY consumer is
  // Sidebar.tsx:129's active-nav highlight — InvoicesList.tsx never reads it, so
  // this opens the UNFILTERED Invoices list (finding F-A, recorded in
  // [PERSONA-01-07], not fixed here per [approvals-filter-not-fixed]). These rows
  // would be visible whether or not the badge is correct, so this catches only a
  // GROSS failure: Approvals opening the wrong screen, or this tenant's rows
  // missing entirely. Clause 1 above carries the weight. Do NOT "strengthen" this
  // into an assertion that the list is narrowed to validated rows — that is a
  // product change wearing a test's clothes ([coverage-honesty]).
  await expect(page.getByTestId('invoices-list')).toBeVisible()
  for (const number of validatedNumbers) {
    const row = invoiceRowByNumber(page, number)
    await expect(row).toBeVisible()
    await expect(row.getByTestId('invoice-status-badge')).toContainText('VALIDATED')
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 3 -- the sidebar roster, per persona (Core AC 7, clause 1)
// ---------------------------------------------------------------------------------------
// EXACT ORDERED equality, never toContain: presence and absence are pinned in one shot.
// The firm has Clients and Customers and NO Approvals; in-house has Approvals and neither
// Clients nor Customers. A nav item silently vanishing for one persona -- or appearing for
// the wrong one -- is precisely this story's defect class, and nothing else in the suite
// would catch it. This is also the honest home of the four firm `nav-only` coverage cells
// (e2e/personas.ts): it proves those surfaces EXIST for the firm without claiming to have
// opened them.
//
// No fixtures: the roster renders regardless of data. approvalsItem is unconditionally in
// navGroups (Sidebar.tsx:125); only its BADGE is conditional.
//
// Left on the config's 60s default even though it is the only 60s test doing TWO full
// sign-ins: it creates no fixtures, makes no writes, and runs third, by which point Test 1
// has warmed the fleet. Raise it in-file if a real run proves otherwise -- never by
// widening the config.
test('sidebar roster: the firm and in-house personas render different, exact nav rosters', async ({ page }) => {
  const errors = collectErrors(page)

  // Two sign-ins on one page, the pattern auth.spec.ts:89-102 already proves works: the
  // ?persona= hand-off overrides the stored session rather than losing to it.
  await signInAs(page, 'firm')
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  await expect
    .poll(() => sidebarRoster(page), { message: 'firm sidebar roster' })
    .toEqual(FIRM_ROSTER)

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  await expect
    .poll(() => sidebarRoster(page), { message: 'in-house sidebar roster' })
    .toEqual(INHOUSE_ROSTER)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 4 -- entity scoping differs by persona (Core AC 7, clause 2)
// ---------------------------------------------------------------------------------------
test('entity scoping: in-house Invoices is tenant-wide, firm Invoices follows the selected client', async ({ page }) => {
  // Two personas, four entities, four invoices and two full sign-ins on one page.
  test.setTimeout(120_000)

  const errors = collectErrors(page)
  const stamp = Date.now()

  const inhouseToken = await login(PERSONAS.B)
  const inhouseEntityA = await createEntity(inhouseToken, { name: `PERSONA-01 scope IH-A ${stamp}`, tin: freshTin() })
  const inhouseEntityB = await createEntity(inhouseToken, { name: `PERSONA-01 scope IH-B ${stamp}`, tin: freshTin() })
  const inhouseNumberA = `INV-P01-SCOPE-IHA-${stamp}`
  const inhouseNumberB = `INV-P01-SCOPE-IHB-${stamp}`
  await createInvoice(inhouseToken, { entity_id: inhouseEntityA.id, ...cleanInvoiceFields(inhouseNumberA) })
  await createInvoice(inhouseToken, { entity_id: inhouseEntityB.id, ...cleanInvoiceFields(inhouseNumberB) })

  const firmToken = await login(PERSONAS.A)
  const firmEntityA = await createEntity(firmToken, { name: `PERSONA-01 scope FM-A ${stamp}`, tin: freshTin() })
  const firmEntityB = await createEntity(firmToken, { name: `PERSONA-01 scope FM-B ${stamp}`, tin: freshTin() })
  const firmNumberA = `INV-P01-SCOPE-FMA-${stamp}`
  const firmNumberB = `INV-P01-SCOPE-FMB-${stamp}`
  await createInvoice(firmToken, { entity_id: firmEntityA.id, ...cleanInvoiceFields(firmNumberA) })
  await createInvoice(firmToken, { entity_id: firmEntityB.id, ...cleanInvoiceFields(firmNumberB) })

  // --- in-house: NOT entity-scoped ---------------------------------------------------
  // InvoicesList.tsx:69 passes entity_id=undefined for in-house, and gateByActiveEntity
  // returns the rows untouched (`if (isInhouse) return rows`, lib/invoices.ts:750). So both
  // entities' invoices belong in ONE list. The list is ORDER BY created_at DESC, id DESC
  // with the handler's default limit of 50, so this run's fixtures are always on page 1
  // however large the tenant grows.
  await signInAs(page, 'inhouse')
  // Identity guard before touching the nav, the same one Tests 2 and 3 take: without it a
  // slow or failed persona swap surfaces as an opaque timeout on a locator further down
  // rather than as "the wrong workspace rendered".
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())
  await goTo(page, 'Invoices')
  await expect(page.getByTestId('invoices-list')).toBeVisible()
  await expect(invoiceRowByNumber(page, inhouseNumberA)).toBeVisible()
  await expect(invoiceRowByNumber(page, inhouseNumberB)).toBeVisible()

  // --- firm: entity-scoped, presence AND absence -------------------------------------
  // Selecting is mandatory, not incidental: Overview/Invoices are CLIENT-scoped
  // ([dashboard-scope-per-client]) and the default selection is clients[0] by name ASC,
  // which is never this test's stamped entity. entity_id is applied SERVER-side, so
  // entity B's invoice is absent from the response, not merely hidden.
  await signInAs(page, 'firm')
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  await selectEntity(page, firmEntityA.name)
  await goTo(page, 'Invoices')
  await expect(page.getByTestId('invoices-list')).toBeVisible()
  await expect(invoiceRowByNumber(page, firmNumberA)).toBeVisible()
  await expect(invoiceRowByNumber(page, firmNumberB)).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
