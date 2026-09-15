// PERSONA-01-03 (Backlog task-272): the in-house app-surface sweep, the Approvals badge
// oracle, and the two persona-DIFFERENCE assertions (sidebar roster, entity scoping).
//
// Why this file exists: every functional browser spec in this package drove the FIRM
// persona. The in-house persona appeared in exactly two assertions in the whole suite
// (auth.spec.ts's identity-switch regression and import-wizard.spec.ts's
// [inhouse-can-start]), neither of which navigates past the surface it lands on -- so an
// in-house-only surface could break in production without a single test going red. That is
// this story's defect class, and the tests below are the guard against it -- the first four
// are PERSONA-01-03's own; Test 5 (task-327), Test 6 (APPR-12-05, task-530) and Test 7
// (APPR-12-07, task-532) arrived later and carry their own headers explaining why they live
// in this file. Test 7 must stay LAST -- its own header says why.
//
// Fixtures are created through e2e/api/client.ts BEFORE the browser is driven, never
// through the UI: [write-path-deferred] fences the in-house WRITE path (import filing /
// manual-entry persistence) out of this story, and the server enforces it anyway (the
// commit gate needs an entity_id, and inhouseClient() pins active.entityId to null --
// lib/clients.ts). The same "own entity per test" discipline as invoice-surfaces.spec.ts
// and import-wizard.spec.ts: this suite runs serially (fullyParallel:false, workers:1,
// playwright.topology.config.ts) against the one deployment all three suites share, with no
// reset between them (docs/e2e-convention.md "One browser, serial").
//
// COUNT ASSERTIONS: tenant B's invoices accumulate through the run -- the api suite writes
// to it before this suite starts, and a retry re-runs a test over its own first attempt's
// rows -- so no assertion here may name a literal count. Only two shapes are legal, and
// both are used below -- (1) compared against a LIVE API read taken in the same test (the
// Approvals badge, the Overview KPI), and (2) containment of rows this test itself created.
// A hardcoded '3' would pass on a clean fixture and fail the moment anything ran first.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { createEntity, createInvoice, getInvoice, listInvoices, login, rollup, validateInvoice, PERSONAS } from '../api/client'
import { ensureFirmPolicyActive } from '../api/contract-helpers'
import { freshTin } from '../api/fixtures'
import { collectErrors, sidebarRoster, signInAs } from '../personaSession'
import { rectsOverlap, WIDE_WIDTHS } from './layout'
import { APP_URL, FIRM_PERSONA, INHOUSE_PERSONA } from './targets'

// cleanInvoiceFields(): a local copy of invoice-surfaces.spec.ts:127-141 (that file exports
// nothing). The FLAT wire shape POST /v1/invoices takes -- supplier_tin/vat as strings --
// NOT a nested validation-engine envelope; internal/invoice/payload.go's
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

// approvableInvoiceFields(): cleanInvoiceFields' ABOVE-THRESHOLD sibling, and the reason
// Test 7 can build a queue at all. internal/demopolicy's root step is
// `condition total > 100000` (demopolicy.go's condAmount), so cleanInvoiceFields' 1,075
// takes the ELSE lane and auto-approves -- excluded from awaiting_approval by construction,
// and pinned there on purpose by TestSeed_InhouseCanFileFixtureStaysOnTheAutoapproveLane.
// Still zero violations: vat is exactly 7.5% of subtotal and the one line item reconciles
// to it. No rule caps the amount.
function approvableInvoiceFields(invoiceNumber: string) {
  return {
    ...cleanInvoiceFields(invoiceNumber),
    subtotal: '200000',
    vat: '15000',
    total: '215000',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '20000', line_total: '200000' }],
  }
}

// createValidatedInvoice(): create + validate, with the fixture guard the whole Approvals
// oracle rests on. POST .../validate is THE gate that earns `validated` (the transitions
// endpoint 409s on that target, internal/invoice/handlers.go), and a zero-violation invoice
// auto-promotes inside it. A blocking violation comes back as a 200 carrying violations as
// DATA, never an HTTP error -- so without this assertion a rule-set change would silently
// degrade the fixture to `draft` and Test 2 would fail much later with an unreadable
// message about a badge that never rendered.
//
// `fields` defaults to the below-threshold clean fixture every earlier test wants; Test 7
// passes approvableInvoiceFields() instead. Validation is also what ARMS the approval run
// (internal/invoice/store.go calls approval.ArmTx in the promoting tx), so which builder is
// used decides which lane the row lands on.
async function createValidatedInvoice(
  token: string,
  entityId: string,
  invoiceNumber: string,
  fields: ReturnType<typeof cleanInvoiceFields> = cleanInvoiceFields(invoiceNumber),
): Promise<string> {
  const created = await createInvoice(token, { entity_id: entityId, ...fields })
  const validated = await validateInvoice(token, created.id)
  expect(
    validated.status,
    `the clean fixture must promote draft->validated; if this fails the rule set moved under cleanInvoiceFields()`,
  ).toBe('validated')
  return created.id
}

function sidebar(page: Page) {
  return page.locator('aside.pf-sidebar')
}

// navButton(): a sidebar nav item by its rendered label. Scoped to nav.pf-nav-list, not just
// the aside, because the firm company switcher is also a <button> in the aside whose
// accessible name embeds the selected entity's name (Sidebar.tsx:149-161) -- a substring
// match against a fixture named e.g. "... approvals ..." resolves to both. Playwright
// matches `name` as a case-insensitive SUBSTRING by default, which is what makes this work
// on a badged item too: the Approvals button's accessible name is "Approvals <count>" once
// its badge renders.
function navButton(page: Page, label: string) {
  return sidebar(page).locator('nav.pf-nav-list').getByRole('button', { name: label })
}

// dashboard serialises to bare `/`; an exact-href match, not a loose /\/$/ regex that would
// pass on any trailing-slash path.
const NAV_URL: Record<string, string | RegExp> = {
  Overview: new URL('/', APP_URL).href,
  Invoices: /\/invoices$/,
  Approvals: /\/approvals$/,
  Rules: /\/rules$/,
  Customers: /\/customers$/,
  Reports: /\/reports$/,
  Workflows: /\/workflows$/,
  Clients: /\/clients$/,
  Audit: /\/audit$/,
  Settings: /\/settings\/members$/,
}

async function goTo(page: Page, label: string): Promise<void> {
  await navButton(page, label).click()
  await expect(page, `goTo(${label}) did not update the URL`).toHaveURL(NAV_URL[label])
}

// navLabelSpan()/navIconSpan(): the two unclassed spans a nav button renders around its
// glyph (Sidebar.tsx:234-235). getByText resolves to the INNERMOST element carrying that
// exact text, so it lands on the label span even when a badge sibling makes the button's
// own text longer; :has(svg) isolates the icon wrapper the same way, since that is the
// only span with an <svg> descendant.
function navLabelSpan(page: Page, label: string) {
  return navButton(page, label).getByText(label, { exact: true })
}

function navIconSpan(page: Page, label: string) {
  return navButton(page, label).locator('span:has(svg)')
}

// invoiceRowByNumber(): filter({ has }) with an EXACT text match, not { hasText } -- every
// fixture number below carries the same Date.now() stamp, so a substring filter could match
// two rows whose numbers share a prefix.
function invoiceRowByNumber(page: Page, invoiceNumber: string) {
  return page.getByTestId('invoice-row').filter({ has: page.getByText(invoiceNumber, { exact: true }) })
}

// The Approvals queue's row, same exact-text discipline as invoiceRowByNumber above.
function approvalRowByNumber(page: Page, invoiceNumber: string) {
  return page.getByTestId('approval-row').filter({ has: page.getByText(invoiceNumber, { exact: true }) })
}

// Mirrors approvalSelectRowLabel (frontend/app/src/lib/approvals.ts). Restated rather than
// imported: this package has no dependency on the app's source, the same reason
// cleanInvoiceFields above is a copy.
function selectRowLabel(invoiceNumber: string): string {
  return `Select invoice ${invoiceNumber}`
}

// awaitingNumbers(): the server's own awaiting_approval set, by invoice number. Membership
// only -- the set also holds the seeded rows and whatever earlier runs left, so no caller
// may read a size off it. The list is ORDER BY created_at DESC with the handler's default
// limit of 50, so a run's own fixtures are always on page 1.
async function awaitingNumbers(token: string): Promise<Set<string>> {
  const res = await listInvoices(token, { awaiting_approval: true })
  return new Set(res.invoices.map((i) => i.invoice_number))
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
// max-height (Sidebar.tsx:155), so once a firm tenant owns more entities than fit the
// viewport, the option for a late-sorting name is unreachable and this click fails with
// "outside of the viewport". Bounded today by per-PR ephemeral environments; a product fix,
// not a test one.
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// The two rosters, derived from Sidebar.tsx's navGroups x glyphs.tsx (each NavDef's
// label). Firm mode splits into a CLIENT group of 6 and a FIRM-WIDE group of 4;
// sidebarRoster() flattens both in DOM order, which is the right shape for the claim being
// made -- the grouping is presentation, the ROSTER is which surfaces this persona has.
const FIRM_ROSTER = ['Overview', 'Invoices', 'Approvals', 'Rules', 'Customers', 'Reports', 'Workflows', 'Clients', 'Audit', 'Settings']
const INHOUSE_ROSTER = ['Overview', 'Invoices', 'Workflows', 'Rules', 'Approvals', 'Reports', 'Audit', 'Settings']

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
  // in-house fetch. Each gets its own freshTin() -- business_entities has a per-tenant
  // unique TIN index, and the seeded portfolio is already sitting in it.
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
  // returns rollup.totals unchanged for in-house (lib/dashboard.ts:234-247, branch :235),
  // and the tile value is the sum over all seven status counts (DashboardActive.tsx:101).
  const live = await rollup(token)
  const invoiceTotal = Object.values(live.totals.counts).reduce((a, b) => a + b, 0)
  expect(invoiceTotal, 'the fixtures above must leave this tenant with invoices to count').toBeGreaterThan(0)
  // No data-testid on this dashboard ([no-testids-on-portfolio-dashboard]); `.pf-dash-row-a
  // > .pf-grid-2` is the KPI grid and is unique on the screen. The title match is an
  // ANCHORED REGEX on purpose: hasText with a plain string is a case-insensitive substring
  // match, so a bare 'Invoices' would blow strict mode against any future sibling tile
  // whose label also contains the word.
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

  // --- Workflows ------------------------------------------------------------------------
  // LIVE, and deliberately thin. APPR-09 wired this list to internal/approval, so everything
  // this block used to assert below the subtitle -- the count, the status pills, the
  // `scope · summary` lines, the `Updated` stamps and the two policy NAMES -- was transcribed
  // from a frontend constant. internal/demopolicy seeds an active policy onto BOTH persona
  // tenants now (plus an in-house-only draft), but neither matches the polH1/polF1 tree
  // shapes those deleted assertions named, so those strings still describe rows that do not
  // exist. They went with the import (APPR-09-08).
  //
  // The firm/in-house DISJOINTNESS proof went with them rather than being re-derived: this
  // list is a tenant-scoped SERVER read now, so a firm policy cannot appear in it by
  // construction and the assertion would be tautological. RLS proves it where it is a real
  // claim -- at the wire, in api/contract-approvals.spec.ts's cross-tenant test.
  //
  // What survives is seed-independent by construction, and is what the in-house
  // NAV_WORKFLOWS coverage cell (personas.ts) now rests on. The FIRM half of this screen,
  // including the whole write path, is topology/workflows.spec.ts, which never signs in as
  // in-house.
  await goTo(page, 'Workflows')
  await expect(page.getByRole('heading', { level: 1, name: 'Approval policies', exact: true })).toBeVisible()
  // active.short === the tenant name here: shortName()'s LEGAL_SUFFIX (/\s+(ltd\.?|limited|
  // plc)$/i, lib/clients.ts) does not strip "Group".
  await expect(
    page.getByText(`Who must sign off before ${INHOUSE_PERSONA.tenantName} transmits an invoice.`, { exact: true }),
  ).toBeVisible()
  // The heading and the subtitle both render ABOVE the ladder (WorkflowsView.tsx), so both
  // pass while the policies fetch is still in flight or has errored. This is the only
  // surviving assertion that requires THIS tenant's fetch to have LANDED, which is the
  // difference between `drives` and a bare mount. Either terminal arm satisfies it -- an
  // empty workspace is a legal state here -- so it stays seed-independent. Same locator
  // topology/workflows.spec.ts settles on.
  await expect(
    page.locator('[data-testid="policies-list"], [data-testid="policies-empty"]'),
    'the in-house policies fetch must land, not merely the screen mount',
  ).toBeVisible()

  // --- Rules ----------------------------------------------------------------------------
  // Pins MOCK FIXTURE BEHAVIOUR (GOLDEN_RULES / GOLDEN_SET, lib/rules.ts), not a backend
  // contract -- the golden ruleset rendered here is static app data, unrelated to the live
  // MBS rule set the engine evaluates. [workflows-included]'s trade-off.
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
  // Pins STATIC COPY (SETTINGS_TABS, data.tsx:285-290), not a backend contract --
  // members/connectors/API keys/certificates are all mock. [workflows-included]'s trade-off.
  //
  // Members arrived as a FOURTH tab, first in the strip and the default one Settings opens
  // on; Roles is the FIFTH, sitting directly after it. The subtitle moved with each of them
  // -- it gained a leading "People," and then a "roles," -- and the loop gained both labels.
  // Kept as an exact-copy assertion rather than loosened to a substring: this surface has no
  // backend to check against, so the copy IS the contract.
  //
  // What the Roles tab RENDERS is topology/roles.spec.ts's, in both modes; this loop only
  // proves it is present for the in-house persona.
  await goTo(page, 'Settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Settings', exact: true })).toBeVisible()
  await expect(page.getByText('WORKSPACE CONFIGURATION', { exact: true })).toBeVisible()
  await expect(page.getByText('People, roles, integrations, developer access, and signing certificates.', { exact: true })).toBeVisible()
  for (const tab of ['Members', 'Roles', 'ERP connectors', 'API & webhooks', 'Signing & certificates']) {
    await expect(page.getByRole('button', { name: tab, exact: true })).toBeVisible()
  }

  // The eighth in-house surface, Approvals, is swept by Test 2 below -- it needs its own
  // fixtures and its own live-read oracle, and folding it in here would bury the one
  // assertion this story's AC 3 actually turns on.

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 2 -- the in-house Approvals badge and surface (Core AC 3)
// ---------------------------------------------------------------------------------------
// Approvals stopped being in-house-exclusive at APPR-12-05: the firm carries it too, in
// the CLIENT group. Test 6 below is this test's firm sibling.
test('Approvals: the in-house badge equals the live awaiting-approval count, and the surface opens', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.B)
  const stamp = Date.now()
  const entity = await createEntity(token, { name: `PERSONA-01 approvals ${stamp}`, tin: freshTin() })
  // TWO validated fixtures, not one: with a single invoice a badge that renders a boolean
  // ("1" whenever anything is awaiting approval) would be indistinguishable from a real count.
  const validatedNumbers = [`INV-P01-APPR-1-${stamp}`, `INV-P01-APPR-2-${stamp}`]
  for (const number of validatedNumbers) {
    await createValidatedInvoice(token, entity.id, number)
  }

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())

  // --- Clause 1: LOAD-BEARING. The badge is asserted EQUAL to a live API read. ------------
  // Same endpoint, same field, same tenant on both sides: the Sidebar calls
  // getRollup -> GET /api/dashboard/v1/rollup (lib/dashboard.ts:102-104), scopedBucket()
  // returns rollup.totals for in-house (:234-247, in-house branch at :235), and the badge
  // is bucket.awaiting_approval (Sidebar.tsx:88); rollup(token) below hits the identical
  // route and reads the identical field. The browser's in-house session mints a token for
  // the same subject/tenant as PERSONAS.B (frontend/app/src/auth.ts).
  const live = await rollup(token)
  const expectedAwaiting = live.totals.awaiting_approval
  // The badge is ABSENT, not "0", when the count is zero (Sidebar.tsx:88) -- so this guard
  // is what keeps the assertion below from being vacuous rather than mere defensiveness.
  // awaiting_approval only counts validated invoices an ACTIVE approval policy blocks, so
  // this is the guard that goes red on an environment whose seeded policy or whose runs
  // were wiped. Its non-zero leg is proved by
  // TestSeed_AwaitingApprovalIsNonZeroAndBelowValidated.
  expect(expectedAwaiting, 'the seeded active policy must leave >=1 invoice awaiting approval, else the badge never renders').toBeGreaterThan(0)
  // The second guard is the one a single live read cannot supply. An unarmed validated
  // invoice satisfies awaiting_approval's NOT EXISTS clause vacuously (dashboard/store.go),
  // so the two fields differ only by the seeder's one APPROVED run -- and while they differ,
  // a badge still wired to counts.validated cannot satisfy the assertion below.
  expect(expectedAwaiting, 'awaiting_approval must differ from counts.validated, else this oracle cannot tell the two fields apart').not.toBe(live.totals.counts.validated)
  // The badge span is the only className="mono" inside a nav button (Sidebar.tsx:236-240);
  // the glyph beside it is an inline <svg> carrying no text, so this cannot pick up the
  // label. Scoped to the aside; in-house renders no company switcher, so every in-scope
  // button is a nav button.
  const approvalsBadge = navButton(page, 'Approvals').locator('.mono')
  await expect(approvalsBadge).toHaveText(String(expectedAwaiting))

  // --- The click did something: the active-nav state moves to Approvals. ------------------
  // Since APPR-12-05 nav('approvals') sets view='approvals' outright and Sidebar's
  // activeNav is just `view`. The highlight carries no class, aria-current or data
  // attribute -- Sidebar.tsx expresses it purely as inline style. font-weight is the
  // discriminator used here rather than the accent bar's colour, which resolves to a
  // design-system token whose serialized value is a moving target. Asserted as a
  // TRANSITION (before -> after) and PAIRED against Invoices, so "everything is bold"
  // cannot pass it.
  const approvalsNav = navButton(page, 'Approvals')
  const invoicesNav = navButton(page, 'Invoices')
  await expect(approvalsNav, 'Approvals is not the active nav item before the click').toHaveCSS('font-weight', '500')
  await approvalsNav.click()
  await expect(approvalsNav, 'Approvals became the active nav item').toHaveCSS('font-weight', '600')
  await expect(invoicesNav, 'and Invoices did not').toHaveCSS('font-weight', '500')

  // --- Clause 2: the click opened the APPROVALS SCREEN, not the Invoices list. ------------
  // This assertion was impossible before APPR-12 and the old [coverage-honesty] comment
  // said so: nav('approvals') used to set view='invoices' + filter='Pending', ctx.filter's
  // ONLY consumer was Sidebar.tsx's active-nav highlight, and InvoicesList.tsx never read
  // it — so Approvals rendered the UNFILTERED Invoices list and the two screens were
  // indistinguishable in the DOM (finding F-A, [PERSONA-01-07]). APPR-12-03 gave Approvals
  // its own view and its own listAwaitingApproval fetch, and APPR-12-05 deleted the alias,
  // so the narrowing is now the PRODUCT's and asserting it is no longer a product change
  // wearing a test's clothes. F-A is closed; [approvals-filter-not-fixed] is retired.
  // Clause 1 above still carries the count; this pins WHICH screen opened.
  await expect(page.getByTestId('approvals-list')).toBeVisible()
  await expect(page.getByTestId('invoices-list')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 3 -- the sidebar roster, per persona (Core AC 7, clause 1)
// ---------------------------------------------------------------------------------------
// EXACT ORDERED equality, never toContain: presence, absence AND slot are pinned in one
// shot. The firm has Clients and Customers; in-house has neither. Both carry Approvals
// since APPR-12-05, but in DIFFERENT slots -- firm directly after Invoices, in-house after
// Rules -- which only an ordered assertion can hold (see Sidebar.tsx's placement-rule
// comment on why the two deliberately diverge). A nav item silently vanishing for one
// persona, appearing for the wrong one, or sliding to the other's slot is precisely this
// story's defect class, and nothing else in the suite would catch it. This is also the
// honest home of the firm `nav-only` coverage cell (e2e/personas.ts): it proves that
// surface EXISTS for the firm without claiming to have opened it.
//
// No fixtures: the roster renders regardless of data. approvalsItem is unconditionally in
// both branches of navGroups; only its BADGE is conditional.
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

// cornerRadii(): all four computed corner radii together, so a uniform token proves itself
// uniform rather than being asserted from one corner alone (BUG-17-06).
async function cornerRadii(loc: Locator): Promise<{ tl: string; tr: string; bl: string; br: string }> {
  return loc.evaluate((el) => {
    const cs = getComputedStyle(el)
    return { tl: cs.borderTopLeftRadius, tr: cs.borderTopRightRadius, bl: cs.borderBottomLeftRadius, br: cs.borderBottomRightRadius }
  })
}

// ---------------------------------------------------------------------------------------
// Test -- the company card, firm switcher vs in-house chip (BUG-17-06, Core AC A-4)
// ---------------------------------------------------------------------------------------
// The switcher carries no `pf-btn` class (Sidebar.tsx's own comment: its `!important` pill
// radius would beat `--radius-input`) -- proving that, and that the global rule still bites
// on the Sign out button that DOES carry it despite its own inline `--radius-sm`, is what
// makes "the chip matches the switcher" a claim about a shared token rather than a
// coincidence of two rounded boxes.
test("company card: the firm switcher renders the in-house chip's corner, and the button rule still makes pills", async ({ page }, testInfo) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')

  const switcher = page.getByTestId('company-switcher')
  const switcherBox = await switcher.boundingBox()
  expect(switcherBox, 'company-switcher never rendered').toBeTruthy()
  const switcherRadii = await cornerRadii(switcher)
  const switcherCornerValues = Object.values(switcherRadii)
  const [firstCorner, ...restCorners] = switcherCornerValues
  for (const corner of restCorners) {
    expect(corner, `company-switcher corners must be uniform: ${JSON.stringify(switcherRadii)}`).toBe(firstCorner)
  }
  const switcherRadiusPx = parseFloat(firstCorner)
  expect(switcherRadiusPx, 'company-switcher radius did not parse to a finite px value').toBeGreaterThanOrEqual(0)
  expect(
    switcherRadiusPx,
    `the switcher's corner (${switcherRadiusPx}px) must be less than half its own height (${switcherBox!.height}px) -- it must not render as a pill`,
  ).toBeLessThan(switcherBox!.height / 2)

  const signOut = page.getByRole('button', { name: 'Sign out' })
  const signOutBox = await signOut.boundingBox()
  expect(signOutBox, 'the Sign out button never rendered').toBeTruthy()
  const signOutRadii = await cornerRadii(signOut)
  const signOutRadiusPx = parseFloat(signOutRadii.tl)
  expect(
    signOutRadiusPx,
    `Sign out's corner (${signOutRadiusPx}px) must be at least half its height (${signOutBox!.height}px) -- the global .pf-btn rule still renders a pill over its own inline --radius-sm`,
  ).toBeGreaterThanOrEqual(signOutBox!.height / 2)

  const closedBorder = await switcher.evaluate((el) => getComputedStyle(el).borderTopColor)
  await switcher.click()
  await expect(page.getByTestId('company-switcher-option').first()).toBeVisible()
  // border-color transitions over --dur-fast; a bare read right after the click can still
  // catch the closed value mid-transition.
  await expect
    .poll(() => switcher.evaluate((el) => getComputedStyle(el).borderTopColor), {
      message: 'the switcher border colour never transitioned away from its closed value',
    })
    .not.toBe(closedBorder)
  await switcher.click()

  await signInAs(page, 'inhouse')
  const chip = page.getByTestId('company-chip')
  const chipBox = await chip.boundingBox()
  expect(chipBox, 'company-chip never rendered').toBeTruthy()
  const chipRadii = await cornerRadii(chip)
  expect(chipRadii, "the in-house chip's corners must equal the firm switcher's").toEqual(switcherRadii)
  expect(
    Math.abs(chipBox!.width - switcherBox!.width),
    `chip width (${chipBox!.width}) and switcher width (${switcherBox!.width}) must match within 1px`,
  ).toBeLessThanOrEqual(1)
  // Heights are not asserted equal -- a <button> and a <div> resolve `line-height: normal`
  // against different font metrics (control font vs --font-sans), so equal height is not
  // guaranteed on a correct build. Attached so the numbers outlive the run.
  await testInfo.attach('company-card-heights.json', {
    body: JSON.stringify({ switcherHeight: switcherBox!.height, chipHeight: chipBox!.height }),
    contentType: 'application/json',
  })

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

// ---------------------------------------------------------------------------------------
// Test 5 -- nav glyph/icon-column alignment (task-327, BUG-01-01, AC-3/AC-4)
// ---------------------------------------------------------------------------------------
// Sidebar.tsx wraps each nav glyph in an UNSIZED span, so a glyph's own width leaks into
// where its label starts. Scope choice: FIRM_ROSTER, which spans BOTH firm nav groups
// (CLIENT: Overview/Invoices/Approvals/Rules/Customers/Reports, FIRM-WIDE:
// Workflows/Clients/Settings) -- required, not incidental: NAV_CLIENTS (the
// 14px forked glyph) lives in FIRM-WIDE, so scoping to the CLIENT group alone would miss
// the reported defect.
//
// Cannot be run locally -- this package's vitest projects run in `node` with no DOM
// layer (docs/e2e-convention.md:71-73) -- so this cannot go RED in a local run; its first
// green run is the post-deploy gate (dev-env.yml). Today's pre-fix values, measured
// against the deployed build: label x = 45 (Clients), 48 (every other item) -- distinct
// icon widths leaking into distinct label offsets.
test('nav-alignment: every sidebar nav item renders its label and icon column at an identical x', async ({ page }) => {
  const errors = collectErrors(page)

  await signInAs(page, 'firm')
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())

  const labelX: number[] = []
  const iconX: number[] = []
  const iconWidth: number[] = []
  for (const label of FIRM_ROSTER) {
    const labelBox = await navLabelSpan(page, label).boundingBox()
    const iconBox = await navIconSpan(page, label).boundingBox()
    expect(labelBox, `${label}: label span not found`).toBeTruthy()
    expect(iconBox, `${label}: icon column not found`).toBeTruthy()
    labelX.push(labelBox!.x)
    iconX.push(iconBox!.x)
    iconWidth.push(iconBox!.width)
  }

  const [firstLabelX, ...restLabelX] = labelX
  for (const x of restLabelX) {
    expect(x, `label x must match every other nav item's (${JSON.stringify(labelX)})`).toBe(firstLabelX)
  }
  const [firstIconX, ...restIconX] = iconX
  for (const x of restIconX) {
    expect(x, `icon-column x must match every other nav item's (${JSON.stringify(iconX)})`).toBe(firstIconX)
  }
  const [firstIconWidth, ...restIconWidth] = iconWidth
  for (const width of restIconWidth) {
    expect(width, `icon-column width must match every other nav item's (${JSON.stringify(iconWidth)})`).toBe(firstIconWidth)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 6 -- Approvals opens for the FIRM persona too (APPR-12-05, task-530)
// ---------------------------------------------------------------------------------------
// What e2e/personas.ts's firm NAV_APPROVALS `drives` cell is paid for. Test 3 proves the
// item is PRESENT in the firm roster; this proves clicking it opens the Approvals screen
// rather than the Invoices list, which is the whole point of deleting the alias.
//
// The queue is empty BY CONSTRUCTION, not by assumption about the environment: this test
// creates its own firm entity, selects it, and that entity is seconds old and owns no
// invoices, so nothing can be awaiting approval under it. (internal/demopolicy now arms BOTH
// persona tenants, including this one -- the hedge that follows came true -- so it is this
// test's own fresh entity, not tenant-wide emptiness, that keeps the queue empty.) The
// empty rung is still the Approvals screen: its own eyebrow, its own h1, its own empty
// testid, and no invoices-list anywhere.
test('firm Approvals: the nav item opens the Approvals screen, not the Invoices list', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `PERSONA-01 firm approvals ${Date.now()}`, tin: freshTin() })

  await signInAs(page, 'firm')
  await expect(sidebar(page)).toContainText(FIRM_PERSONA.tenantName.toUpperCase())
  // Mandatory, same reason as Test 4: Approvals is CLIENT-scoped, and the default
  // selection is clients[0] by name ASC, never this test's own entity.
  await selectEntity(page, entity.name)

  await goTo(page, 'Approvals')
  await expect(page.getByRole('heading', { level: 1, name: 'Approvals', exact: true })).toBeVisible()
  await expect(page.getByText('AWAITING YOUR APPROVAL', { exact: true })).toBeVisible()
  await expect(page.getByTestId('approvals-empty')).toBeVisible()
  // The alias is gone: opening Approvals must not render the Invoices list.
  await expect(page.getByTestId('invoices-list')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// APPROVALS_GRID_COLUMNS is '24px 140px 1fr 130px 90px 180px 110px' (ApprovalsView.tsx):
// seven tracks, one of them fluid. The indices below name what the two layout claims are
// about; neither claim ever asserts one of these px values.
const APPROVALS_TRACKS = 7
const BUYER_TRACK = 2
const FIXED_TRACKS = [0, 1, 3, 4, 5, 6]

// gridCells(): the x and width of each of a grid container's seven direct children, in
// track order. The count assertion is load-bearing: it is what makes an index-by-index
// comparison meaningful at all. Track 0 is the bare CHECKBOX in the head, but a row's
// stretched approval-select-cell wrapper (BUG-17-04/05) -- invariant either way, which is
// all the claims below need. A blocked row's reason renders inside track 1, never as an
// eighth child (see Test 7's header).
async function gridCells(container: Locator, label: string): Promise<Array<{ x: number; width: number }>> {
  const cells = container.locator('> *')
  await expect(cells, `${label}: a grid built from APPROVALS_GRID_COLUMNS renders exactly ${APPROVALS_TRACKS} cells`).toHaveCount(APPROVALS_TRACKS)
  const measured: Array<{ x: number; width: number }> = []
  for (let i = 0; i < APPROVALS_TRACKS; i++) {
    const box = await cells.nth(i).boundingBox()
    expect(box, `${label}: track ${i} never rendered`).toBeTruthy()
    measured.push({ x: box!.x, width: box!.width })
  }
  return measured
}

// centerY(): the vertical midpoint of a Playwright boundingBox() rect.
function centerY(box: { y: number; height: number }): number {
  return box.y + box.height / 2
}

// elementFromPointMatches(): true when the element painting at (x, y) is `testId` itself or
// a descendant -- the only honest oracle for paint order/clipping (demo-persona.spec.ts's
// own elementFromPoint idiom).
async function elementFromPointMatches(page: Page, x: number, y: number, testId: string): Promise<boolean> {
  return page.evaluate(
    ({ x, y, testId }) => document.elementFromPoint(x, y)?.closest(`[data-testid="${testId}"]`) != null,
    { x, y, testId },
  )
}

// ---------------------------------------------------------------------------------------
// Test -- a firm row this seat cannot approve (BUG-17-06, Core AC A-2/A-3). Declared
// immediately before Test 7, which must stay last by declaration.
// ---------------------------------------------------------------------------------------
// PERSONAS.A (subject ...0001) holds only cfo on the firm tenant (db/seed.dev.sql); the
// firm plan arms fin_mgr then compliance below its ₦250m/₦1bn conditions
// (demopolicy.go:132-146), so a below-threshold fixture leaves this seat blocked at the
// first step without ever reaching one it holds. ensureFirmPolicyActive restores the
// seeded policy first -- roles.spec.ts/workflows.spec.ts run in the same serial suite and
// may have deleted or drafted over it.
test('firm Approvals: a row the seat cannot approve shows the reason icon, no sentence line, and opens its invoice', async ({ page }) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  await ensureFirmPolicyActive(token)
  const stamp = Date.now()
  const entity = await createEntity(token, { name: `BUG-17 reason icon ${stamp}`, tin: freshTin() })
  const number = `INV-B17-REASON-${stamp}`
  const id = await createValidatedInvoice(token, entity.id, number)

  // --- fixture guard, at the wire, before the browser is driven --------------------------
  const wire = await getInvoice(token, id)
  expect(
    wire.can_approve,
    `${number} must be blocked for this seat; if this fails ensureFirmPolicyActive did not restore the seeded policy`,
  ).toBe(false)
  const reason = wire.approve_blocked_reason
  expect(reason, `${number} must carry a blocked reason`).toBeTruthy()
  expect(reason!.length, 'the reason sentence must be a real message, not a stub').toBeGreaterThanOrEqual(20)
  expect((await awaitingNumbers(token)).has(number), `${number} must be awaiting approval`).toBe(true)

  await signInAs(page, 'firm')
  await selectEntity(page, entity.name)
  await goTo(page, 'Approvals')
  const row = approvalRowByNumber(page, number)
  await expect(row.getByTestId('approval-select-row')).toBeDisabled()

  // --- no sentence line, one grid line ----------------------------------------------------
  const list = page.getByTestId('approvals-list')
  const headCellCount = await list.locator('.pf-list-head').locator('> *').count()
  expect(headCellCount, 'the list head rendered no cells').toBeGreaterThan(0)
  await expect(row.locator('> *')).toHaveCount(headCellCount)
  // innerText, not textContent: the always-mounted, hidden tip keeps the sentence in
  // textContent even while closed.
  expect(await row.innerText(), 'the blocked sentence must never render as visible text').not.toContain(reason!)

  const rowCellsLoc = row.locator('> *')
  const rowCellCount = await rowCellsLoc.count()
  const cellHeights: number[] = []
  for (let i = 0; i < rowCellCount; i++) {
    const box = await rowCellsLoc.nth(i).boundingBox()
    expect(box, `cell ${i} never rendered`).toBeTruthy()
    cellHeights.push(box!.height)
  }
  const tallestCell = Math.max(...cellHeights)
  const rowBox = await row.boundingBox()
  expect(rowBox, 'the row never rendered').toBeTruthy()
  const rowMetrics = await row.evaluate((el) => {
    const cs = getComputedStyle(el)
    return { paddingTop: parseFloat(cs.paddingTop), paddingBottom: parseFloat(cs.paddingBottom), borderBottom: parseFloat(cs.borderBottomWidth) }
  })
  const contentHeight = rowBox!.height - rowMetrics.paddingTop - rowMetrics.paddingBottom - rowMetrics.borderBottom
  expect(
    Math.abs(contentHeight - tallestCell),
    `the row must hold a single grid line: content height ${contentHeight} vs tallest cell ${tallestCell}`,
  ).toBeLessThanOrEqual(1)

  // --- icon placement, inside the Invoice # cell, clear of the truncating number ----------
  const icon = row.getByTestId('approval-blocked-icon')
  await expect(icon).toBeVisible()
  const num = row.getByText(number, { exact: true })
  const cell1 = row.locator('> *').nth(1)
  expect(
    await num.evaluate((e) => e.scrollWidth > e.clientWidth),
    `${number} must overflow its track, or the placement claims below are vacuous`,
  ).toBe(true)

  let iconBox = await icon.boundingBox()
  const numBox = await num.boundingBox()
  const cell1Box = await cell1.boundingBox()
  expect(iconBox, 'the icon never rendered').toBeTruthy()
  expect(numBox, 'the number span never rendered').toBeTruthy()
  expect(cell1Box, 'the Invoice # cell never rendered').toBeTruthy()
  expect(iconBox!.x, "the icon must sit at or after the number's right edge").toBeGreaterThanOrEqual(numBox!.x + numBox!.width - 0.5)
  expect(rectsOverlap(iconBox!, numBox!), 'the icon must not overlap the truncating number').toBe(false)
  expect(Math.abs(centerY(iconBox!) - centerY(numBox!)), "the icon must sit on the number's own line").toBeLessThanOrEqual(1)
  expect(iconBox!.x + iconBox!.width, 'the icon must stay inside the Invoice # cell').toBeLessThanOrEqual(cell1Box!.x + cell1Box!.width + 0.5)
  expect(
    await elementFromPointMatches(page, iconBox!.x + iconBox!.width / 2, centerY(iconBox!), 'approval-blocked-icon'),
    'the icon must not be covered by the truncating text',
  ).toBe(true)

  // --- screen reader -----------------------------------------------------------------------
  await expect(icon).toHaveAccessibleDescription(reason!)

  // --- mouse, below --------------------------------------------------------------------------
  await icon.hover()
  const tip = row.getByTestId('approval-blocked-tip')
  await expect(tip).toBeVisible()
  await expect(tip).toHaveText(reason!)
  const viewport = page.viewportSize()
  expect(viewport, 'no viewport size available').toBeTruthy()
  let tipBox = await tip.boundingBox()
  expect(tipBox, 'the tip never rendered').toBeTruthy()
  expect(tipBox!.x, 'the tip must sit inside the viewport').toBeGreaterThanOrEqual(0)
  expect(tipBox!.y, 'the tip must sit inside the viewport').toBeGreaterThanOrEqual(0)
  expect(tipBox!.x + tipBox!.width, 'the tip must sit inside the viewport').toBeLessThanOrEqual(viewport!.width + 0.5)
  expect(tipBox!.y + tipBox!.height, 'the tip must sit inside the viewport').toBeLessThanOrEqual(viewport!.height + 0.5)
  expect(tipBox!.y, 'the tip must render below the icon').toBeGreaterThanOrEqual(iconBox!.y + iconBox!.height - 0.5)
  expect(
    await elementFromPointMatches(page, tipBox!.x + tipBox!.width / 2, centerY(tipBox!), 'approval-blocked-tip'),
    'the queue list must not clip the tip',
  ).toBe(true)
  const tipHeight = tipBox!.height
  await page.mouse.move(0, 0)
  await expect(tip).toBeHidden()

  // --- mouse, flip above when the viewport ends 8px under the icon ------------------------
  expect(iconBox!.y, 'the icon must sit far enough down the page for the flip to be provable').toBeGreaterThan(tipHeight + 8)
  const entryViewport = page.viewportSize()
  expect(entryViewport, 'no entry viewport available').toBeTruthy()
  try {
    await page.setViewportSize({ width: entryViewport!.width, height: Math.ceil(iconBox!.y + iconBox!.height + 8) })
    await icon.hover()
    await expect(tip).toBeVisible()
    iconBox = await icon.boundingBox()
    expect(iconBox, 'the icon vanished after the resize').toBeTruthy()
    tipBox = await tip.boundingBox()
    expect(tipBox, 'the tip never rendered after the resize').toBeTruthy()
    expect(
      tipBox!.y + tipBox!.height,
      'a broken flip leaves the tip below, past the viewport bottom',
    ).toBeLessThanOrEqual(iconBox!.y + 0.5)
    expect(tipBox!.y, 'the flipped tip must still sit inside the viewport').toBeGreaterThanOrEqual(0)
    expect(
      await elementFromPointMatches(page, tipBox!.x + tipBox!.width / 2, centerY(tipBox!), 'approval-blocked-tip'),
      'the flipped tip must not be clipped',
    ).toBe(true)
  } finally {
    await page.setViewportSize(entryViewport!)
  }

  // --- keyboard: Tab reaches the icon, the disabled checkbox before it is skipped ---------
  await page.mouse.move(0, 0)
  await expect(tip).toBeHidden()
  await icon.focus()
  await page.keyboard.press('Shift+Tab')
  await expect(icon).not.toBeFocused()
  await expect(row.getByTestId('approval-select-row'), 'the disabled checkbox must never take focus').not.toBeFocused()
  await page.keyboard.press('Tab')
  await expect(icon).toBeFocused()
  await expect(tip).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(tip).toBeHidden()

  // --- non-navigation: the icon and the select cell's own area never open the invoice ----
  await icon.click()
  await expect(page).toHaveURL(/\/approvals$/)

  const selectCell = row.getByTestId('approval-select-cell')
  const selectCheckbox = row.getByTestId('approval-select-row')
  const selectCellBox = await selectCell.boundingBox()
  const checkboxBox = await selectCheckbox.boundingBox()
  expect(selectCellBox, 'the select cell never rendered').toBeTruthy()
  expect(checkboxBox, 'the checkbox never rendered').toBeTruthy()
  const clickX = selectCellBox!.x + selectCellBox!.width - 2
  const clickY = selectCellBox!.y + selectCellBox!.height / 2
  expect(clickX, 'the click point must sit outside the checkbox').toBeGreaterThan(checkboxBox!.x + checkboxBox!.width + 1)
  expect(
    await page.evaluate(({ x, y }) => document.elementFromPoint(x, y)?.getAttribute('data-testid'), { x: clickX, y: clickY }),
    'the click point must resolve to the select cell, not the checkbox',
  ).toBe('approval-select-cell')
  // A real Chromium click on a disabled checkbox fires no listener at all -- this click
  // lands on the select cell's own area, which is the only oracle for its stopPropagation.
  await page.mouse.click(clickX, clickY)
  await expect(page.getByTestId('approvals-list')).toBeVisible()
  await expect(page).toHaveURL(/\/approvals$/)

  // --- the Buyer cell opens the invoice, and Back returns ---------------------------------
  await row.locator('> *').nth(BUYER_TRACK).click()
  await expect(page).toHaveURL(new RegExp(`/invoices/${id}$`))
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(/\/approvals$/)
  await expect(row).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// ---------------------------------------------------------------------------------------
// Test 7 -- the in-house approval queue journey, and the APPROVALS_GRID_COLUMNS layout
// assertions (APPR-12-07, task-532: A07-1..A07-3, A07-5..A07-7)
// ---------------------------------------------------------------------------------------
// LAST BY DECLARATION ORDER, and it must stay last. This suite is workers:1 /
// fullyParallel:false, so Playwright runs declaration order, and Test 2's badge oracle
// (`expectedAwaiting > 0`) must read the queue BEFORE this journey drains part of it.
//
// THE FIXTURE IS ABOVE THRESHOLD ON PURPOSE -- see approvableInvoiceFields. The API guard
// below asserts each created row actually reached the queue, so a future threshold move
// fails as "the fixture left the approval lane" rather than as an unreadable timeout on a
// row that never rendered.
//
// THREE controls, each answering a different way this journey could pass on a broken
// screen:
//   - a BELOW-THRESHOLD row, absent from the queue. Without it the narrowing step only
//     asserts presence, which any unfiltered list satisfies.
//   - an above-threshold row this run creates and never TICKS, still on the queue after
//     the bulk approve. Without it, "approved every row on the page" passes.
//   - the wire re-read at the end. Without it, a client-side optimistic removal passes the
//     DOM assertion -- which is the exact claim A07-3 is making about this screen.
//
// NEVER `approvals-select-all`, here or in any later journey. The queue also holds the
// seeded DEMO-2026-9001/9002/9003 rows, all approvable by this persona; approvals are
// permanent (epic Q12) and demopolicy's armBacklog anti-join skips an invoice already
// carrying an approved run, so a later boot does NOT restore them -- select-all would kill
// Test 2's oracle for the rest of that deployment's life. Rows are ticked individually, by
// their own aria-label.
//
// The reason renders inside track 1 (BlockedReason, ApprovalsView.tsx), never as an eighth
// child -- so the grid claims below hold at exactly 7 children whether or not a row is
// blocked. Blocked-row coverage lives in the firm test above and this test's own Emeka leg
// below (BUG-17-06); this journey's own armed rows stay approvable throughout the sweep.
//
// Cannot be run locally, same as Test 5: every Playwright config in this package is
// deliberately webServer-less and points at deployed URLs, so its first real run -- red or
// green -- is the post-deploy gate (dev-env.yml).
test('in-house Approvals: the queue narrows, bulk approve settles per item, and the refetch confirms it', async ({ page }, testInfo) => {
  // Four fixtures with their own validate round trips, four viewport sweeps and a
  // two-request fan-out, plus BUG-17-06's row-open/seat-switch extension. Same in-file
  // headroom precedent as Tests 1 and 4.
  test.setTimeout(150_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.B)
  const stamp = Date.now()
  const entity = await createEntity(token, { name: `APPR-12 queue ${stamp}`, tin: freshTin() })
  // TWO above-threshold rows to approve, so the per-item results panel is asserted on a
  // set; ONE more this run leaves untouched; ONE below-threshold row. See the header.
  const approveNumbers = [`INV-A12-QUEUE-1-${stamp}`, `INV-A12-QUEUE-2-${stamp}`]
  const untickedNumber = `INV-A12-UNTICKED-${stamp}`
  const belowThresholdNumber = `INV-A12-BELOW-${stamp}`
  const onQueue = [...approveNumbers, untickedNumber]
  const idsByNumber = new Map<string, string>()
  for (const number of onQueue) {
    const created = await createValidatedInvoice(token, entity.id, number, approvableInvoiceFields(number))
    idsByNumber.set(number, created)
  }
  const untickedId = idsByNumber.get(untickedNumber)
  if (!untickedId) throw new Error(`${untickedNumber} was created but its id was not captured`)
  await createValidatedInvoice(token, entity.id, belowThresholdNumber)

  // --- The fixture guard, at the wire, before the browser is driven ----------------------
  // Both halves of the narrowing, read from the server's own awaiting_approval predicate
  // (internal/invoice/store.go). Containment only -- the queue also holds the seeded rows
  // and whatever earlier runs left, and no assertion here may name a total.
  const queuedBefore = await awaitingNumbers(token)
  for (const number of onQueue) {
    expect(
      queuedBefore.has(number),
      `${number} must be awaiting approval; if this fails the fixture left the approval lane (demopolicy's total > 100000 threshold moved, or the seeded policy is no longer active)`,
    ).toBe(true)
  }
  expect(
    queuedBefore.has(belowThresholdNumber),
    `${belowThresholdNumber} is below demopolicy's threshold, so the policy's else lane auto-approves it and it must never reach the queue`,
  ).toBe(false)

  await signInAs(page, 'inhouse')
  await expect(sidebar(page)).toContainText(INHOUSE_PERSONA.tenantName.toUpperCase())

  // --- A07-1: the queue is the real filtered set, not the Invoices list -------------------
  await goTo(page, 'Approvals')
  await expect(page.getByTestId('approvals-list')).toBeVisible()
  await expect(page.getByTestId('invoices-list')).toHaveCount(0)
  for (const number of onQueue) {
    await expect(approvalRowByNumber(page, number), `${number} is awaiting approval and must be on the queue`).toBeVisible()
  }
  await expect(
    approvalRowByNumber(page, belowThresholdNumber),
    'the auto-approved row must be absent -- the narrowing is the server\'s, not a presence check',
  ).toHaveCount(0)

  // --- A07-5 / A07-6: what APPROVALS_GRID_COLUMNS actually encodes -------------------------
  // RELATIONSHIPS, never a raw dimension (layout.ts's header: BUG-03-05's own `width <=
  // 1082` check was SATISFIED by the 588px it stranded). Both claims are made at all four
  // WIDE_WIDTHS, widest first, and both need a POPULATED queue -- which is why they live in
  // this journey and not in Test 6's empty firm surface.
  //
  // assertFillsColumn is NOT used here, and the three reasons are worth writing down.
  // (1) Mechanically its default slackPx = 24 throws against this view's own 36px
  // horizontal inset -- `approvals-list` is not the padded root the way `invoice-detail`
  // is (ApprovalsView.tsx puts the padding on an unnamed wrapper). (2) Substantively "the
  // table fills its column" does not test this constant at all: swapping the `1fr` for a
  // fixed track leaves the list div full-width while the TRACKS stop early, so the fill
  // claim stays green on the defect. (3) Raising the slack past 36 does not rescue it --
  // the |left - right| claim it would then rest on is off by whatever gutter `.pf-scroll`
  // (overflow-y:auto) takes out of its own content box, which is a function of how many
  // rows the queue holds rather than of the constant under test. The two claims below
  // measure INSIDE the grid, so a gutter cancels out of both.
  const head = page.getByTestId('approvals-list').locator('.pf-list-head')
  const entryViewport = page.viewportSize()
  const sweep: Array<{ viewport: number; rowWidth: number; cells: number[] }> = []
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      await expect
        .poll(() => page.evaluate(() => window.innerWidth), { message: `the viewport never reached ${width}px` })
        .toBe(width)

      const headCells = await gridCells(head, 'the list head')
      for (const [rowIndex, number] of onQueue.entries()) {
        const row = approvalRowByNumber(page, number)
        const rowBox = await row.boundingBox()
        expect(rowBox, `${number}: the row disappeared mid-sweep`).toBeTruthy()
        const rowCells = await gridCells(row, number)

        // A07-6 -- ONE track definition, not two. The head and every body row are SEPARATE
        // grid containers consuming the same constant, which is exactly the two-literal
        // drift ClientsView carries and APPROVALS_GRID_COLUMNS' own comment rejects. Per
        // track, left edge against left edge; 1px is sub-pixel rounding, and a second
        // literal drifting out of step moves whole columns.
        for (const [i, headCell] of headCells.entries()) {
          expect(
            Math.abs(headCell.x - rowCells[i].x),
            `track ${i}: the head and ${number} disagree on where it starts at ${width}px (head ${headCell.x}, row ${rowCells[i].x}) -- two track definitions, not one`,
          ).toBeLessThanOrEqual(1)
        }

        if (rowIndex === 0) sweep.push({ viewport: width, rowWidth: rowBox!.width, cells: rowCells.map((c) => c.width) })
      }
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  // A07-5 -- the `1fr` absorbs the window. Six fixed tracks stay put while the Buyer track
  // takes every pixel the window gives the row.
  expect(sweep.length, 'the sweep measured nothing, so every comparison below is vacuous').toBe(WIDE_WIDTHS.length)
  const widest = sweep[0]
  const narrowest = sweep[sweep.length - 1]
  const rowGrowth = widest.rowWidth - narrowest.rowWidth
  // ANTI-VACUITY, and the reason this is an assertion rather than a comment: a cap anywhere
  // above the grid freezes every delta below at zero, which would satisfy the invariance
  // claim AND the absorption claim while the queue sat in a dead band.
  expect(
    rowGrowth,
    `the row did not grow between ${narrowest.viewport}px and ${widest.viewport}px, so something above the grid is capping it and the two claims below are vacuous`,
  ).toBeGreaterThan(0)
  for (const fit of sweep) {
    for (const i of FIXED_TRACKS) {
      expect(
        Math.abs(fit.cells[i] - widest.cells[i]),
        `track ${i} is a FIXED track and must not move with the window: ${fit.cells[i]} at ${fit.viewport}px vs ${widest.cells[i]} at ${widest.viewport}px`,
      ).toBeLessThanOrEqual(1)
    }
  }
  const buyerGrowth = widest.cells[BUYER_TRACK] - narrowest.cells[BUYER_TRACK]
  expect(
    Math.abs(buyerGrowth - rowGrowth),
    `the Buyer track must absorb ALL of the row's growth: it took ${buyerGrowth}px of the row's ${rowGrowth}px between ${narrowest.viewport}px and ${widest.viewport}px`,
  ).toBeLessThanOrEqual(1)
  await testInfo.attach('approvals-grid-sweep.json', {
    body: JSON.stringify(sweep, null, 2),
    contentType: 'application/json',
  })

  // --- A07-2: select individually, arm, confirm -------------------------------------------
  // INDIVIDUALLY. `approvals-select-all` would also tick the seeded rows -- see this test's
  // header for why that is unrecoverable -- and it would take untickedNumber with it,
  // collapsing the selection-binding control below.
  for (const number of approveNumbers) {
    await approvalRowByNumber(page, number).getByLabel(selectRowLabel(number), { exact: true }).check()
  }
  // An enabled checkbox click reaches the row's own handler in jsdom (B17-D3's premise) but
  // must never navigate in the real browser -- the wrapper's stopPropagation is what stops it.
  await expect(page).toHaveURL(/\/approvals$/)
  const bar = page.getByTestId('approvals-bulk-bar')
  await expect(bar).toBeVisible()
  // This run's OWN selection, not a fixture-volume count.
  await expect(bar).toContainText(`${approveNumbers.length} selected on this page`)

  await page.getByTestId('approvals-bulk-submit').click()
  // Arming renders the confirm section INSIDE the same bar -- never a modal ([no-modal]).
  await expect(bar).toContainText(`Approve ${approveNumbers.length} invoices?`)
  const confirm = page.getByTestId('approvals-bulk-confirm')
  await expect(confirm).toBeVisible()
  await confirm.click()

  // Per-item, never a headline count: a count cannot say WHICH invoice was refused. The
  // outcome is matched EXACTLY -- 'Not approved' contains 'Approved' as a substring.
  const results = page.getByTestId('approvals-results')
  await expect(results).toBeVisible()
  await expect(results.getByTestId('approval-result-row')).toHaveCount(approveNumbers.length)
  for (const number of approveNumbers) {
    const resultRow = results.getByTestId('approval-result-row').filter({ has: page.getByText(number, { exact: true }) })
    await expect(resultRow, `${number} must appear exactly once in the results panel`).toHaveCount(1)
    await expect(resultRow.getByText('Approved', { exact: true }), `${number} must have been approved`).toBeVisible()
  }

  // --- A07-3: the refetch is the affirmation ---------------------------------------------
  // Nothing is optimistic -- list.run() after settle is what removes these rows. The
  // unticked row is what makes this a NARROWING rather than an emptying: it proves the
  // list rung re-rendered with content and that only the ticked rows were sent.
  for (const number of approveNumbers) {
    await expect(approvalRowByNumber(page, number), `${number} was approved and must leave the queue`).toHaveCount(0)
  }
  await expect(page.getByTestId('approvals-list'), 'the refetched queue still renders its remaining rows').toBeVisible()
  await expect(
    approvalRowByNumber(page, untickedNumber),
    `${untickedNumber} was never ticked and must still be awaiting approval`,
  ).toBeVisible()
  // The panel is gated on `results !== null` ALONE, outside every `state ===` rung, so it
  // survives the refetch that nulls list.data (G-04-D). That placement is the claim.
  await expect(results, 'the results panel must survive the refetch that removed the rows').toBeVisible()

  // The same three facts at the WIRE, where a client-side removal cannot reach them. The
  // DOM assertions above are what the user sees; this is what the server did.
  const queuedAfter = await awaitingNumbers(token)
  for (const number of approveNumbers) {
    expect(queuedAfter.has(number), `${number} must have left the server's awaiting_approval set, not just the DOM`).toBe(false)
  }
  expect(queuedAfter.has(untickedNumber), `${untickedNumber} must still be awaiting approval on the server`).toBe(true)

  // --- Core AC D-1/D-3 (BUG-17-06): the unticked approvable row opens its own invoice ------
  const untickedRow = approvalRowByNumber(page, untickedNumber)
  await expect(untickedRow).toBeVisible()
  await expect(untickedRow.getByTestId('approval-blocked-icon'), 'an approvable row shows no reason icon').toHaveCount(0)
  await untickedRow.locator('> *').nth(BUYER_TRACK).click()
  await expect(page).toHaveURL(new RegExp(`/invoices/${untickedId}$`))
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(/\/approvals$/)
  await expect(untickedRow).toBeVisible()

  // Recorded here, right before the switch: the approvable row this run created, at the
  // entry viewport, is the only same-row comparison a firm-only fixture can't offer.
  const approvableHeightBox = await untickedRow.boundingBox()
  expect(approvableHeightBox, 'the unticked row never rendered before the seat switch').toBeTruthy()

  // --- Core AC D-1 (BUG-17-06): switch the demo seat to one this row IS blocked for --------
  // Emeka Uzowulu holds only line_mgr (db/seed.dev.sql), never the in-house plan's fin_dir
  // (demopolicy.go:156) -- the only way to reach an in-house blocked row without publishing
  // a second policy ([topology-never-publishes]). No restore: standIn is never persisted
  // (App.tsx), every Playwright test gets a fresh context, and this is the last test.
  await page.getByTestId('persona-trigger').click()
  await expect(page.getByTestId('persona-row-list')).toBeVisible()
  await page.getByTestId('persona-row').filter({ hasText: 'Emeka Uzowulu' }).click()
  await expect(page.getByTestId('persona-toast-title')).toBeVisible()
  await page.getByTestId('persona-toast-dismiss').click()
  await expect(page.getByTestId('persona-name')).toHaveText('Emeka Uzowulu')

  await goTo(page, 'Approvals')
  const blockedRow = approvalRowByNumber(page, untickedNumber)
  await expect(blockedRow).toBeVisible()
  await expect(blockedRow.getByTestId('approval-select-row'), 'the seat really is blocked').toBeDisabled()
  await expect(blockedRow.getByTestId('approval-blocked-icon')).toBeVisible()
  const blockedHeightBox = await blockedRow.boundingBox()
  expect(blockedHeightBox, 'the blocked row never rendered after the seat switch').toBeTruthy()
  expect(
    Math.abs(blockedHeightBox!.height - approvableHeightBox!.height),
    `the blocked row (${blockedHeightBox!.height}px) must stand the same height as the approvable row it replaced (${approvableHeightBox!.height}px)`,
  ).toBeLessThanOrEqual(1)

  await blockedRow.locator('> *').nth(BUYER_TRACK).click()
  await expect(page).toHaveURL(new RegExp(`/invoices/${untickedId}$`))
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(/\/approvals$/)
  await expect(blockedRow).toBeVisible()

  // A07-7
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
