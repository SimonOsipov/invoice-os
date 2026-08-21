// M4-09-06 (task-187): the focused topology e2e for the live invoice list +
// detail surfaces (M4-09-04/M4-09-05) -- mirrors M3-09's validation-playground
// pattern (topology.spec.ts: firm-persona sign-in -> wait for the /v1/me
// verified marker -> drive the live surface). NOT the M4-14 demo script
// ([focused-e2e-topology], out of scope per the M4-09 story).
//
// db/seed.dev.sql now seeds 31 invoices, but only across 6 of its 10 curated
// business_entities (persona-handoff-fix step 4, [demo-invoice-seed]) -- every
// scenario below still creates its OWN entity + invoice(s) via e2e/api/client.ts
// BEFORE driving the UI -- the same "own entity per test" discipline as
// import-wizard.spec.ts (no-duplicate-invoice-number is scoped per entity, and
// this suite runs serially -- fullyParallel:false, workers:1
// (playwright.topology.config.ts, [topology-config-conforms-workers-1] since
// M4-14-01) -- with retries:1 in CI, against the same shared firm-persona
// tenant every other topology spec also drives).
//
// Fixture data mirrors e2e/api/fixtures.ts's badInvoice/validInvoice shapes
// (verified against the seeded v1+v2 rule set, migrations/
// 20260711121327_seed_mbs_v1.sql + 20260716185106_rule_set_v2.sql), but built
// as this service's own FLAT wire shape (supplier_tin/vat/... strings, not the
// nested /v1/validate envelope) -- internal/invoice/payload.go's MBSPayload is
// what nests them again before 04 evaluates, so a flat createInvoice + POST
// .../validate round-trips the identical verdict fixtures.ts's BAD_INVOICE_KEYS
// pins for the nested path.
import { test, expect, type Page, type Request } from '@playwright/test'
import {
  login,
  createEntity,
  createInvoice,
  validateInvoice,
  transitionInvoice,
  approveUntilClosed,
  firmApproverTokens,
  PERSONAS,
} from '../api/client'
import { ensureFirmPolicyActive } from '../api/contract-helpers'
import { freshTin } from '../api/fixtures'
import { buildMixedCsv, buildPerfCsv } from '../importFixtures'
import { approvalRun404Dropper } from './consoleGate'
import { assertFillsColumn, gaps, WIDE_WIDTHS } from './layout'
import { APP_URL, FIRM_PERSONA, VALIDATION_EXPECTED } from './targets'

// [topology-never-publishes] scoped to policy IDENTITY (docs/e2e-convention.md): this
// self-heal restores the tenant's OWN seeded policy, never a new one. Unwrapped (D3
// protocol, ../api/validation.spec.ts:14-26) -- the api run ahead of this one (dev-env.yml)
// leaves the firm tenant's active slot empty (contract-invoice.spec.ts's own armedInvoice
// cleanup), so every approval below would otherwise 404 against an invoice that armed no
// run. A genuine convergence failure must abort this file loudly, not surface as confusing
// per-test 404s.
test.beforeAll(async () => {
  const token = await login(PERSONAS.A)
  await ensureFirmPolicyActive(token)
})

// collectErrors()/signInFirm(): the same console/pageerror + firm-persona
// sign-in idiom topology.spec.ts and import-wizard.spec.ts each inline (no
// spec file in this package exports its own helpers today, so this is a third
// copy, not a new seam).
function collectErrors(page: Page): string[] {
  const errors: string[] = []
  const dropApprovalRun404 = approvalRun404Dropper(page)
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (dropApprovalRun404(msg.text(), msg.location().url)) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

async function signInFirm(page: Page): Promise<void> {
  // The landing page is the single sign-in front door, so the app has no picker to click
  // on a deployed build; ?persona= IS the sign-in, exactly as landing destUrl() hands off.
  const url = `${APP_URL}?persona=${FIRM_PERSONA.param}`
  const res = await page.goto(url)
  expect(res, `no response from ${url}`).toBeTruthy()
  expect(res!.ok(), `${url} returned HTTP ${res!.status()}`).toBeTruthy()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
}

// goToInvoices()/openInvoiceRow(): the two navigation seams every scenario
// below shares. The sidebar's "Invoices" nav button (glyphs.tsx's
// NAV_INVOICES) is matched with a case-sensitive /Invoices/ so the header's
// lowercase "New invoice" CTA can never collide, and is scoped to the sidebar
// <nav> (personaSession.ts:69's own selector) so CreateFlow.tsx's review-step
// `← Invoices` exit cannot either -- a caller arriving straight off an import
// has both on screen. Substring, not exact: the nav button's accessible name
// absorbs its needs_attention badge (Sidebar.tsx's invoicesItem). A row click
// routes through InvoicesList's onClick -> ctx.openImportedInvoice(id) -> the
// SAME live-detail seam an imported invoice uses ([reuse-imported-seam],
// InvoicesList.tsx) -- clicking ANY real invoice's row opens LiveInvoiceDetail,
// not the mock placeholder, so this needs no import-flow detour at all.
async function goToInvoices(page: Page): Promise<void> {
  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: /Invoices/ }).click()
  await expect(page.getByTestId('invoices-list')).toBeVisible()
}

// openInvoiceRow(): fix cycle 1 (M5-09-08, task-256) -- the text match is scoped to the
// `invoices-list` container (InvoicesList.tsx), not the whole page. M5-09-06's
// batch-submit-results panel ALSO renders the invoice number (per-invoice skip reasons are
// Core AC #1), so after a submit BOTH the results panel and the row show the same text --
// a page-wide getByText is a strict-mode violation the moment that's true, and it's real,
// required product behaviour, not something to design around. Scoping to the container is
// also what survives InvoicesList's own post-submit refetch: `invoices-list` fully
// unmounts while list.run()'s GET is in flight (state leaves 'ready', the async hook nulls
// `data`) -- the scoped locator is driven only through a retrying `.click()`, never a
// one-shot read, so it waits out that unmount/remount window instead of resolving against
// a dead node.
async function openInvoiceRow(page: Page, invoiceNumber: string): Promise<void> {
  await page.getByTestId('invoices-list').getByText(invoiceNumber, { exact: true }).click()
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
}

// selectEntity(): [dashboard-scope-per-client] (persona-handoff-fix step 2) made
// Overview/Invoices CLIENT-scoped surfaces (Sidebar.tsx's CLIENT nav group) -- every
// fixture entity this file creates via the API seam must become the ACTIVE workspace
// switcher selection before its own invoices/rollup bucket show up on either surface.
// signInFirm() alone leaves the switcher's default selection at whatever `clients[0]`
// resolves to (portfolio's List `ORDER BY name ASC, id ASC`, internal/portfolio/store.go)
// -- never the fresh entity, which sorts wherever its own Date.now()-suffixed name lands
// among the seeded portfolio and every other entity this run has created. Sidebar.tsx:
// data-testid="company-switcher" (the toggle button) / "company-switcher-option" (each
// row in the open dropdown).
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// A supplier/buyer/line-item shape that ORIGINALLY fired EXACTLY
// ['supplier-tin-format', 'vat-standard-rate'] against the active v2 rule set
// (fixtures.ts's BAD_INVOICE_KEYS): a malformed supplier TIN plus a VAT that isn't 7.5%
// of the subtotal. Every OTHER v1/v2 rule (the required/format/range rules,
// line-items-required, line-items-sum-subtotal, line-cost-non-negative) is satisfied so
// no incidental third violation sneaks in and breaks an exact-key assertion.
//
// REVISED (INVCR-01-16, task-292, AC-10): true only through the STATELESS /v1/validate
// path (unaffected, e2e/api/validation.spec.ts's own use). Every caller HERE goes
// through createInvoice -- and task-293's C7 fix ([supplier-from-entity]) makes
// Store.Create discard any caller-supplied supplier_tin and re-derive it from the
// invoice's own entity -- so 'BADTIN' never reaches storage and supplier-tin-format can
// never fire for either of this file's two createInvoice call sites (the list-surface
// test only checks needs_attention/status badges, unaffected either way; the
// detail-surface test's own exact-key assertion is the one this subtask fixed). Only
// vat-standard-rate genuinely fires through createInvoice now.
function badInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: 'BADTIN',
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '70',
    total: '1070',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// The same invoice with both broken fields corrected (a canonical
// NNNNNNNN-NNNN supplier TIN, VAT at the correct 7.5%) -- fires ZERO
// violations, mirroring fixtures.ts's validInvoice.
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

// M5-09-08 (task-256): outcome control for the submission surface below. The mock APP
// adapter keys its scripted response off the BUYER TIN in the reserved, Luhn-invalid
// 99999999-#### block (internal/submission/mock_script.go's mockAllocations) -- every
// allocated value also matches the shipped buyer-tin-format rule
// (^[0-9]{8}-[0-9]{4}$), so it passes validation and actually reaches submission.
// cleanInvoiceFields()'s own buyer TIN (a fixed, unrelated constant) is never reused for
// these fixtures -- submittableInvoiceFields() overrides it instead of forking the whole
// shape, keeping supplier_tin/subtotal/vat identical (freshTin() + the same
// vat-standard-rate-clean 1000/75 pair) so nothing incidental fires.
const MOCK_TIN_ACCEPT = '99999999-0001'
const MOCK_TIN_REJECT = '99999999-0002'
// Pending -- mockPendingPolls=2 x mockPollAfterSeconds=5s (mock_adapter.go) holds the
// invoice in `submitted` for >=10s, the window C1's non-vacuous history-refresh oracle
// needs. -0001/-0002 both converge synchronously (~800ms) and can't prove live refresh at
// all -- see the reject/resubmit test below.
const MOCK_TIN_PENDING = '99999999-0003'

// internal/invoice/handlers.go's awaitingApprovalReason, byte for byte -- the detail
// page's undecided-state reason (AC-4 below). Own copy, not a cross-spec import (repo
// convention, e2e/api/contract-invoice.spec.ts's own note on the same string).
const AWAITING_APPROVAL_REASON = 'This invoice is waiting on approval — it can be submitted once an approver approves it.'

function submittableInvoiceFields(invoiceNumber: string, buyerTin: string) {
  return { ...cleanInvoiceFields(invoiceNumber), buyer_tin: buyerTin }
}

// lineEditFields(): INVED-01-08's own fixture for the draft line-editor tests (Core AC
// #5) -- two lines summing to 900.00 against a header subtotal of 1000, a mismatch that
// WOULD block validation (line-items-sum-subtotal), which is exactly why this invoice is
// never sent through /validate at all. Never confused with cleanInvoiceFields' own single
// reconciling line.
function lineEditFields(invoiceNumber: string) {
  return {
    ...cleanInvoiceFields(invoiceNumber),
    subtotal: '1000',
    line_items: [
      { description: 'Widget A', quantity: '4', unit_price: '100', line_total: '400' },
      { description: 'Widget B', quantity: '5', unit_price: '100', line_total: '500' },
    ],
  }
}

// invoiceRowByNumber(): the row-index idiom's simpler sibling for these tests -- every
// fixture below gets its own Date.now()-suffixed invoice_number, so an exact-text filter
// on the row is unambiguous without needing to capture and index into the list response
// (unlike the Day-60 arc, which reuses a FIXED recurring number across every run).
// filter({ has }) — not { hasText }, which is a substring match and could collide across
// two Date.now() calls landing in the same millisecond.
function invoiceRowByNumber(page: Page, invoiceNumber: string) {
  return page.getByTestId('invoice-row').filter({ has: page.getByText(invoiceNumber, { exact: true }) })
}

// submitSelected(): arms via batch-submit, confirms via batch-submit-confirm, then waits
// for the POST .../invoices/submissions response. Unlike a list GET, this URL is
// unambiguous -- a poll tick never POSTs ([waitForResponse-on-the-list-is-poll-ambiguous]
// only applies to the list's GET) -- so this needs none of that care. Shared by every
// submit click below: the happy-path test's only submit, and the reject test's initial
// submit and its resubmit leg.
async function submitSelected(page: Page): Promise<void> {
  const resp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices/submissions'),
  )
  await page.getByTestId('batch-submit').click()
  await page.getByTestId('batch-submit-confirm').click()
  await resp
}

// assertFiscalRecord(): the C8-strength proof this isn't a stub. A `data:image/png;base64,`
// prefix plus a present <img> is not proof of a RENDERED image -- expect.poll on
// naturalWidth survives decode latency a bare evaluate() wouldn't. And "IRN != invoice
// number" passes trivially: mockIdentifiersFor (mock_script.go) builds the IRN as
// mockDocRef(env.ID, digest) + "-FBMOCK01-" + YYYYMMDD, and env.ID IS
// Canonical.InvoiceNumber (mock_wire.go:53) -- the IRN always CONTAINS the (possibly
// truncated) invoice number, so the inequality alone is guaranteed by the suffix, not by
// the number being genuinely absent. The real proof is the deterministic suffix shape.
async function assertFiscalRecord(page: Page, invoiceNumber: string): Promise<void> {
  const irn = page.getByTestId('fiscal-irn')
  await expect(irn).toBeVisible()
  const irnText = (await irn.textContent())?.trim() ?? ''
  expect(irnText.length, 'fiscal-irn must be non-empty').toBeGreaterThan(0)
  expect(irnText, 'the IRN must not equal the bare invoice number').not.toBe(invoiceNumber)
  expect(irnText, 'IRN shape <sanitised id>-FBMOCK01-YYYYMMDD (mock_script.go mockIdentifiersFor)').toMatch(/-FBMOCK01-\d{8}$/)

  const csid = page.getByTestId('fiscal-csid')
  await expect(csid).toBeVisible()
  const csidText = (await csid.textContent())?.trim() ?? ''
  expect(csidText.length, 'fiscal-csid must be non-empty').toBeGreaterThan(0)

  const qr = page.getByTestId('fiscal-qr')
  await expect(qr).toBeVisible()
  await expect(qr).toHaveAttribute('src', /^data:image\/png;base64,/)
  await expect.poll(() => qr.evaluate((el) => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)

  // CSID is an unbroken base64 string with no natural wrap points -- the real regression
  // oracle for BUG-03-05's word-break fix. IRN's mock suffix already gives hyphen breaks.
  const card = page.getByTestId('fiscal-record-card')
  const cardBox = await card.boundingBox()
  expect(cardBox, 'fiscal-record-card must be visible').toBeTruthy()
  for (const testId of ['fiscal-irn', 'fiscal-csid']) {
    const el = page.getByTestId(testId)
    const box = await el.boundingBox()
    expect(box, `${testId} must be visible`).toBeTruthy()
    expect(box!.x + box!.width, `${testId} must not overflow fiscal-record-card`).toBeLessThanOrEqual(cardBox!.x + cardBox!.width + 2)
    // boundingBox is blind to this: a stretched flex child's box stays fixed
    // regardless of content, so unbroken text just overflows it invisibly.
    // scrollWidth vs clientWidth measures the element's own content instead.
    const overflow = await el.evaluate((node) => node.scrollWidth - node.clientWidth)
    expect(overflow, `${testId} text must not overflow its own box (word-break)`).toBeLessThanOrEqual(1)
  }
}

test('list surface: real rows render with real status badges, and Needs attention re-fetches server-side', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-09 list ${Date.now()}`, tin: freshTin() })

  // attn: created with the bad fixture, then validated via the API (not the
  // UI -- this test is the LIST surface, not the detail fix loop the second
  // scenario below drives). A blocking violation on a draft invoice enters
  // needs_attention (internal/invoice/needs_attention_test.go's
  // matchesNeedsAttentionPredicate: rejected matches, failed matches unless kept;
  // a draft matches on a severity:"error" violation or a newest approval run that
  // closed 'rejected' -- attn never promotes past draft, so it never arms a run at all;
  // clean below does arm one under the active firm policy, but 'open' isn't 'rejected').
  const attnNumber = `INV-M409-ATTN-${Date.now()}`
  const attn = await createInvoice(token, { entity_id: entity.id, ...badInvoiceFields(attnNumber) })
  await validateInvoice(token, attn.id)

  // clean: created with the fixed fixture, then validated -- zero violations
  // promotes it draft->validated, which is definitely NOT needs_attention.
  const cleanNumber = `INV-M409-CLEAN-${Date.now()}`
  const clean = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(cleanNumber) })
  await validateInvoice(token, clean.id)

  await signInFirm(page)
  // Invoices is CLIENT-scoped now ([dashboard-scope-per-client]) -- see selectEntity's
  // own doc comment for why this is required, not optional, before goToInvoices.
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const attnRow = page.getByTestId('invoice-row').filter({ hasText: attnNumber })
  const cleanRow = page.getByTestId('invoice-row').filter({ hasText: cleanNumber })

  await expect(attnRow).toBeVisible()
  await expect(attnRow.getByTestId('invoice-status-badge')).toContainText('DRAFT')
  await expect(cleanRow).toBeVisible()
  await expect(cleanRow.getByTestId('invoice-status-badge')).toContainText('VALIDATED')

  // Needs attention ON: re-fetches GET .../invoices?needs_attention=true
  // ([server-side-needs-attention], InvoicesList.tsx) -- the predicate is
  // applied server-side, not re-derived in the browser, so `clean` (no
  // blocking violation) must vanish from the DOM entirely, not just render
  // greyed out.
  const filteredResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('needs_attention') === 'true',
  )
  await page.getByTestId('needs-attention-toggle').click()
  await filteredResp
  await expect(attnRow).toBeVisible()
  await expect(cleanRow).toHaveCount(0)

  // Toggling back off re-fetches the unfiltered list -- `clean` reappears.
  const unfilteredResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('needs_attention') === null,
  )
  await page.getByTestId('needs-attention-toggle').click()
  await unfilteredResp
  await expect(cleanRow).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// RED (task-332, BUG-01-06, Mode A) -- hasBlockingViolation's disclosure chip doesn't
// exist yet. Reuses the same blocked/clean fixture shape as the list-surface test above
// (badInvoiceFields fires exactly one error-severity violation, vat-standard-rate; the
// invoice stays draft).
test('register-disclosure: a blocked draft and a clean draft render differently', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-01-06 disclosure ${Date.now()}`, tin: freshTin() })

  const blockedNumber = `INV-BUG0106-BLOCKED-${Date.now()}`
  const blocked = await createInvoice(token, { entity_id: entity.id, ...badInvoiceFields(blockedNumber) })
  await validateInvoice(token, blocked.id)

  const cleanNumber = `INV-BUG0106-CLEAN-${Date.now()}`
  const clean = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(cleanNumber) })
  await validateInvoice(token, clean.id)

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const blockedRow = page.getByTestId('invoice-row').filter({ hasText: blockedNumber })
  const cleanRow = page.getByTestId('invoice-row').filter({ hasText: cleanNumber })
  await expect(blockedRow).toBeVisible()
  await expect(cleanRow).toBeVisible()

  // AC-1/AC-2: derived from `violations` alone (one error-severity violation here), never
  // a re-derivation of needs_attention or a status arm.
  await expect(blockedRow).toContainText(/1 ERROR\b/)
  await expect(cleanRow).not.toContainText(/\d+ ERRORS?\b/)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('detail surface: violations render against the rule-set version, the fix loop clears them, and status history records both the round trip and the not-a-draft regression', async ({
  page,
}) => {
  // Multiple live round trips through the detail surface (not-validated -> 3x
  // Re-validate + 1 edit + a remount) on a possibly cold fleet -- default 60s
  // is tight for that many sequential awaits, mirroring import-wizard's own
  // headroom bump for its own multi-round-trip test.
  test.setTimeout(90_000)

  const errors = collectErrors(page)

  // INVED-01-08 (AC #2/#3): every POST .../validate the browser fires, registered BEFORE
  // either of the two Re-validate clicks below so step 6's disabled-button assertion can
  // prove genuine non-vacuity -- validatePosts must already hold both requests before the
  // disabled click is even attempted.
  const validatePosts: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/validate')) validatePosts.push(r.url())
  })

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-09 detail ${Date.now()}`, tin: freshTin() })

  // Created via the API but left COMPLETELY UNVALIDATED (draft,
  // rule_set_version null) -- the detail surface's own "not yet validated"
  // state (InvoiceDetail.tsx's `not-validated` branch) must be driven by a
  // REAL invoice that has never been evaluated, not synthesized.
  const invoiceNumber = `INV-M409-DETAIL-${Date.now()}`
  const inv = await createInvoice(token, { entity_id: entity.id, ...badInvoiceFields(invoiceNumber) })
  expect(inv.rule_set_version_id, 'a freshly created invoice must start unvalidated').toBeNull()

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  // 1. Not yet validated -- one genesis status-history row (from_status=null,
  //    the INSERT Store.Create makes at creation time, store.go).
  await expect(page.getByTestId('not-validated')).toBeVisible()
  await expect(page.getByTestId('status-history-row')).toHaveCount(1)
  await expect(page.getByTestId('status-history-row').first()).toContainText('Created · draft')

  // 2. First Re-validate: the bad fixture fires exactly BAD_INVOICE_KEYS
  //    (fixtures.ts) -- a blocking violation, so the invoice stays draft (no
  //    promotion, no new history row) while the violations table now renders
  //    the real verdict against the live rule-set version.
  //
  //    RESOLVED (INVCR-01-16, task-292, AC-10): the KNOWN PRE-EXISTING GAP task-293's QA
  //    found (and INVCR-01-18/task-303 deliberately left untouched, outside its own
  //    scope) is fixed here. Store.Create now derives supplier_tin from the entity
  //    ([supplier-from-entity]) rather than trusting badInvoiceFields()'s 'BADTIN', so
  //    'BADTIN' never reaches storage and supplier-tin-format can never fire from this
  //    fixture again. vat-standard-rate is unaffected -- a wrong VAT is a value the
  //    client genuinely controls -- and stays the sole assertion this loop needs; the
  //    fixture stays broken via VAT alone rather than being redesigned around a second
  //    violation (the simpler of AC-10's two named options).
  const violationsTable = page.getByTestId('violations-table')
  await page.getByTestId('revalidate').click()
  await expect(violationsTable).toBeVisible()
  await expect(page.getByTestId('not-validated')).toHaveCount(0)
  await expect(violationsTable).toContainText('vat-standard-rate')
  await expect(violationsTable.locator('tbody tr').first().locator('td').last()).toHaveText(String(VALIDATION_EXPECTED.ruleSetVersion))
  // INVCR-01-16 AC-9: discharges task-289's own deferred empirical check -- v3 fills
  // `target` on vat-standard-rate (blank under v2), so the Path column (ViolationsTable's
  // 4th <td>) must render it here, on this LIVE invoice-detail mount of the table, not
  // the em-dash placeholder a blank target would leave.
  const vatRow = violationsTable.locator('tbody tr').filter({ hasText: 'vat-standard-rate' })
  await expect(vatRow.locator('td').nth(3), 'v3 fills target on vat-standard-rate -- Path must not render the placeholder').not.toHaveText('—')

  // BUG-03-01: at the rail's width the table overflows its wrapper -- the fix makes that
  // wrapper scroll instead of clip, so every column (not just the first three) stays
  // reachable. scrollLeft = scrollWidth drives it fully right; the last header's bounding
  // box must then sit inside the scroll container's, proving it scrolled INTO view rather
  // than merely being present in the (clipped) DOM.
  const scrollBox = page.getByTestId('violations-scroll')
  const overflow = await scrollBox.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
  expect(overflow.scrollWidth, 'the table must overflow its narrow rail wrapper').toBeGreaterThan(overflow.clientWidth)
  await scrollBox.evaluate((el) => {
    el.scrollLeft = el.scrollWidth
  })
  const containerBox = (await scrollBox.boundingBox())!
  const lastHeaderBox = (await violationsTable.getByRole('columnheader', { name: 'Rule-set version' }).boundingBox())!
  expect(lastHeaderBox.x, 'scrolled fully right, Rule-set version must be inside the scroll container').toBeGreaterThanOrEqual(containerBox.x)
  expect(
    lastHeaderBox.x + lastHeaderBox.width,
    'scrolled fully right, Rule-set version must not overflow the scroll container',
  ).toBeLessThanOrEqual(containerBox.x + containerBox.width + 1)

  // CodeRabbit (PR #138): overflow alone doesn't make a container reachable -- browsers
  // don't focus an overflowing div by default. Prove a keyboard user (not just script) can
  // reach the far column: focus the wrapper directly (skips a brittle full-page Tab-order
  // walk) and let the UA's native scrollable-region key handling do the rest.
  await scrollBox.evaluate((el) => {
    el.scrollLeft = 0
  })
  await scrollBox.focus()
  await expect(scrollBox, 'wrapper must be keyboard-focusable, not just scriptable').toBeFocused()
  // End targets scrollTop, not scrollLeft -- this wrapper overflows only horizontally
  // (measured live on PR #138: End left scrollLeft at 0), so ArrowRight is the key that
  // actually proves reachability here. Loop rather than assert an exact pixel increment,
  // since the UA's per-press scroll step isn't a value this test should pin.
  for (let i = 0; i < 20; i++) {
    await page.keyboard.press('ArrowRight')
  }
  await expect(scrollBox, 'ArrowRight must not move focus off the wrapper').toBeFocused()
  const scrollLeftAfterKeys = await scrollBox.evaluate((el) => el.scrollLeft)
  expect(scrollLeftAfterKeys, 'ArrowRight on the focused wrapper must scroll it -- this is what keyboard reachability means').toBeGreaterThan(0)

  // AC-4: Message (td index 1: Severity=0, Message=1, Rule key=2, Path=3, Rule-set
  // version=4 -- same ordinal convention as the Path check above) must not be crushed.
  const messageBox = (await violationsTable.locator('tbody tr').first().locator('td').nth(1).boundingBox())!
  expect(messageBox.width, 'Message must not be crushed at the rail width').toBeGreaterThanOrEqual(160)

  await expect(page.getByTestId('invoice-status-badge')).toContainText('DRAFT')
  await expect(page.getByTestId('status-history-row')).toHaveCount(1)

  // 3. The fix: edit VAT AND -- the priority regression QA flagged -- edit
  //    issue_date with a plain YYYY-MM-DD value, the form's own placeholder
  //    shape. Before the QA fix (commit 0bfc4a1), sending a bare date 400'd
  //    at the backend (editReq.IssueDate decodes into a *time.Time, which
  //    only accepts a full RFC3339 string); diffEditInput now normalizes a
  //    bare date to midnight UTC first. onSaved firing (staleSinceEdit
  //    becoming true, asserted below) is the one behaviour a 400 could never
  //    produce -- a failed submit takes the catch branch and renders a red
  //    inline error instead, never calling onSaved.
  //
  //    Supplier TIN is deliberately NOT edited here anymore (INVCR-01-18,
  //    C7 fix, edit path): the field is no longer an editable form control
  //    at all (InvoiceDetail.tsx renders it display-only, entity-derived) --
  //    Store.Update/Edit now ALWAYS re-derive supplier_tin from the invoice's
  //    entity regardless of what a PATCH sends, mirroring Store.Create's own
  //    [supplier-from-entity] override. A `.fill()` against that input would
  //    now throw ("element is not editable"), not merely assert something
  //    false. Correcting VAT alone is sufficient to reach a clean re-validate
  //    below: the ONLY genuinely-firing violation on this deployed build is
  //    vat-standard-rate (see the KNOWN PRE-EXISTING GAP note above --
  //    supplier-tin-format was never actually blocking post-INVCR-01-17).
  //
  //    The 2 inputs are matched by their own label text via XPath sibling
  //    lookup: the form carries no per-field test ids.
  // [edit-mode-in-body] (INVED-01-07/08): the form now mounts only while `editing`, so the
  // Edit toggle must be clicked before it exists at all -- without this every
  // form.locator(...) below is a locator TIMEOUT, not an assertion.
  await page.getByTestId('edit-toggle').click()
  const form = page.getByTestId('edit-invoice')
  await form.locator('xpath=.//div[normalize-space(text())="Issue date"]/following-sibling::input').fill('2026-02-01')
  await form.locator('xpath=.//div[normalize-space(text())="VAT"]/following-sibling::input').fill('75')
  await page.getByRole('button', { name: 'Save changes' }).click()

  await expect(page.getByTestId('stale-verdict')).toBeVisible()
  // [edit-mode-in-body]: handleSaved's setEditing(false) unmounts the editor on success --
  // absence of the form IS the success oracle. `not.toContainText` on a 0-element locator
  // passes VACUOUSLY (not-found), so it never actually proved anything; toHaveCount(0) does.
  await expect(form, 'a successful save unmounts the editor').toHaveCount(0)

  // 4. handleSaved now refreshes the status-history timeline IN PLACE
  //    (history.run() alongside detail.run(), InvoiceDetail.tsx) -- no
  //    navigation is needed to observe what the server recorded. Editing a
  //    DRAFT invoice never demotes it, so the timeline stays at 1 row.
  await expect(page.getByTestId('status-history-row')).toHaveCount(1)

  // 5. Second Re-validate: now clean -- promotes draft -> validated (a new
  //    history row) and the violations panel flips to the clean-pass message.
  //    handleRevalidate also refreshes the timeline in place (history.run()),
  //    so the promotion is asserted on this SAME mounted detail view -- no
  //    remount required (the earlier list->row remount workaround here was
  //    both unnecessary once the timeline is live and itself flaky, causing a
  //    click-timeout on the invoice-number text match).
  await page.getByTestId('revalidate').click()
  await expect(violationsTable).toContainText('Passes all rules')
  await expect(violationsTable).toContainText(`rule-set v${VALIDATION_EXPECTED.ruleSetVersion}`)
  await expect(page.getByTestId('invoice-status-badge')).toContainText('VALIDATED')
  await expect(page.getByTestId('status-history-row')).toHaveCount(2)
  await expect(page.getByTestId('status-history-row').last()).toContainText('draft → validated')

  // AUDIT-02-07 (AC-1/AC-4): both rows carry the SAME subject -- createInvoice's
  // login(PERSONAS.A) and this page's firm persona are both c0000000-...-0001
  // (targets.ts:27, frontend/app/src/auth.ts:44), whom db/seed.dev.sql:41 names. Count
  // first: an empty locator satisfies every assertion after it.
  const validationActors = page.getByTestId('status-history-actor')
  await expect(validationActors).toHaveCount(2)
  // EXACT, never toContainText: the APP_PERSONAS fall-through renders 'Chinedu Okafor ·
  // Okafor & Partners' (lib/actor.ts), which a substring match accepts. This is what proves
  // on the deployed stack that the server's resolved pair wins, across BOTH writers
  // (Store.Create's genesis row and transitionTx's promotion).
  await expect(validationActors.nth(0)).toHaveText('Chinedu Okafor')
  await expect(validationActors.nth(1)).toHaveText('Chinedu Okafor')
  // Absences, after the positive control so neither can pass on an unrendered card.
  await expect(page.getByTestId('status-history')).not.toContainText('c0000000-0000-0000-0000-000000000001')
  await expect(validationActors.nth(0)).not.toHaveClass(/mono/)

  // 6. INVED-01-07/08: the OLD 409-on-Re-validate dead end is gone. On an untouched
  //    VALIDATED invoice, Re-validate is DISABLED with a visible reason
  //    ([revalidate-visibility]/[revalidate-reason-from-backend]) -- Edit stays enabled
  //    ([D-actions-hidden-while-editing] only hides the bar while an editor is mounted,
  //    which none is here). The wire's own copy is asserted as a substring only (never the
  //    em dash literal -- an encoding hazard through a CI shell).
  const revalidate = page.getByTestId('revalidate')
  await expect(revalidate).toBeVisible()
  await expect(revalidate).toBeDisabled()
  await expect(page.getByTestId('edit-toggle')).toBeEnabled()
  await expect(page.getByTestId('revalidate-blocked-reason')).toContainText('Only draft invoices can be re-validated')

  // Non-vacuity: the recorder must already hold BOTH earlier Re-validate clicks (steps 2
  // and 5) before the disabled click below is attempted -- proving the predicate actually
  // matches POST .../validate requests, not just that none happened to fire.
  expect(validatePosts, 'the two earlier Re-validate clicks were observed').toHaveLength(2)

  const noValidate = page.waitForRequest(
    (r) => r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/validate'),
    { timeout: 2_000 },
  )
  // force:true bypasses Playwright's own actionability pre-checks (which would otherwise
  // refuse to click a disabled element) -- but the real HTML `disabled` attribute still
  // suppresses the browser's own click event, so React's onClick handler never fires.
  // Never dispatchEvent('click'): that bypasses the browser's disabled-button suppression
  // too and would test the OPPOSITE of the guarantee this step exists to prove.
  await revalidate.click({ force: true })
  await expect(noValidate).rejects.toThrow()
  expect(validatePosts, 'a disabled Re-validate must issue no request').toHaveLength(2)
  await expect(page.getByTestId('status-history-row')).toHaveCount(2)
  await expect(page.getByTestId('invoice-status-badge')).toContainText('VALIDATED')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// MixedImportResponse: the subset of POST /v1/imports's success body this test reads to
// get a real invoice_id, mirroring import-wizard.spec.ts's own local (non-exported) type
// of the same name byte-for-byte. NOT imported from that file -- both are *.spec.ts,
// and importing one spec's module graph into another would register its tests twice
// (the same discipline importFixtures.ts's header documents for why buildMixedCsv is a
// plain .ts module outside every testDir).
interface MixedImportResponse {
  invoice_violations: {
    invoice_number: string
    invoice_id?: string
    violations: { rule_key: string }[]
  }[]
}

// M4-14-02 (task-209): the Day-60 moment-of-value, folded into this capability flow
// instead of a new dated demo ([capability-not-date], docs/e2e-convention.md) -- import a
// batch, open one of THOSE failing invoices, fix it inline, re-validate to green, and see
// the dashboard rollup update. Reuses this file's own signInFirm/collectErrors and
// import-wizard.spec.ts's proven mixed-CSV upload recipe (buildMixedCsv/E2E-04) rather
// than re-deriving either. Does NOT reuse goToInvoices()/openInvoiceRow() -- opening the
// invoice here needs a captured invoice_id tied to its row (see step 4 below), which
// those two helpers have no hook for.
//
// Dashboard/Clients carry no data-testid ([no-testids-on-portfolio-dashboard],
// grep-verified) -- selected below by role/exact-text/CSS class, the same idiom
// day30.spec.ts/topology.spec.ts already used for those surfaces before this story split
// them into auth.spec.ts/validation.spec.ts (Dashboard/Clients coverage stayed here, in
// this file's Day-60 arc).
test('Day-60 moment of value: import-batch -> open-failing-invoice -> fix-VAT-inline -> re-validate-to-green -> dashboard rollup updates', async ({
  page,
}) => {
  // Multiple live round trips on a possibly cold fleet -- the import wizard's own
  // preview+import, the detail fix loop's edit+revalidate, and TWO Dashboard/Clients
  // navigations each triggering their own live rollup fetch. Mirrors this file's own
  // "detail surface" test's 90s bump, with extra headroom for the two extra nav round
  // trips this arc adds on top of that flow.
  test.setTimeout(120_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-14 arc ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  // Overview/Invoices are CLIENT-scoped surfaces now ([dashboard-scope-per-client]) --
  // make `entity` the active workspace switcher selection BEFORE anything else, so both
  // this arc's Invoices step (4) and its dashboard-rollup step (7a) actually show ITS
  // data rather than whichever entity `clients[0]` happens to default to (see
  // selectEntity's own doc comment).
  await selectEntity(page, entity.name)

  // 1. Import the mixed batch for the fresh entity -- import-wizard.spec.ts's own proven
  // E2E-04 recipe (select the fresh entity, Read columns, click-map invoice_number +
  // subtotal, Import), reused verbatim rather than re-derived.
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  // [import-upload-unify] no more in-page entity <select> -- selectEntity() above
  // (workspace switcher) already made `entity` the active selection CreateUpload's
  // entityId mirrors, so there is nothing left to pick here.

  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'm4-14-arc.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  const resp = await importResp
  const body = (await resp.json()) as MixedImportResponse

  // 2. The real invoice_id of INV-UI-MIX-VIOLATE, which fires ONLY vat-standard-rate
  // (buildMixedCsv's doc comment; re-verified live at import-wizard.spec.ts:286).
  const violateEntry = body.invoice_violations.find((iv) => iv.invoice_number === 'INV-UI-MIX-VIOLATE')
  expect(violateEntry, 'expected an invoice_violations entry for INV-UI-MIX-VIOLATE').toBeTruthy()
  expect(violateEntry!.violations.map((v) => v.rule_key)).toEqual(['vat-standard-rate'])
  expect(violateEntry!.invoice_id, 'invoice_violations[].invoice_id must be populated on a real import').toBeTruthy()

  // 3. Pre-fix Clients health pill. The import already ran every created row through the
  // validation engine as part of ITS OWN transaction (internal/importer/service.go's
  // ValidateBatch -> Store.ApplyValidation, unlike an API-created draft, which stays
  // unvalidated until an explicit POST .../validate) -- so by now INV-UI-MIX-VIOLATE is
  // already `draft` with one error-severity violation (needs_attention: true,
  // internal/dashboard/store.go's predicate) and INV-UI-MIX-CLEAN is already
  // auto-promoted to `validated` (zero violations always earns the promote-iff-earned
  // step, internal/invoice/store.go) -- never needs_attention regardless of status. This
  // fresh entity therefore has EXACTLY one needs_attention invoice before any fix, so the
  // pre-fix pill is asserted to an exact value, not just captured for a later diff.
  await page.getByRole('button', { name: /Clients/ }).click()
  const clientRow = page.locator('.pf-list-row').filter({ hasText: entity.name })
  // Exact even though this flow now arms an open approval run on validate (the firm tenant
  // is governed, this file's own beforeAll) -- needs_attention's approval arm is
  // draft-with-a-latest-rejected-run only (TestStoreRollup_ApprovalRejectedArmIsDraftOnly,
  // TestStoreRollup_NeedsAttentionIncludesApprovalRejected), and an open run is not that.
  await expect(clientRow, 'fresh entity row must render on Clients before the fix').toContainText('1 NEEDS ATTENTION')

  // 4. Open the violating invoice's live detail. Invoices is a CLIENT-scoped surface now
  // ([dashboard-scope-per-client]) -- the list narrows to the ACTIVE entity in the
  // browser (filterByActiveEntity, lib/invoices.ts; the backend endpoint itself stays
  // tenant-global, [D8], internal/invoice/handlers.go), and `selectEntity` above already
  // made `entity` the active one. "INV-UI-MIX-VIOLATE" is a FIXED invoice_number
  // buildMixedCsv() recreates on every run -- including import-wizard.spec.ts's own
  // E2E-04/05/09 -- but every one of those runs used ITS OWN fresh entity, so THIS
  // entity's filtered view can only ever contain the two rows this run just imported.
  // The row-index disambiguation this arc used before entity-scoping existed (capture
  // the list response, find the imported invoice_id's array index, click that DOM index)
  // is therefore no longer needed -- a plain exact-text click via this file's own
  // goToInvoices/openInvoiceRow helpers is unambiguous, the same idiom every other test
  // in this file already uses. (Not a re-derivation of import-wizard.spec.ts's E2E-05,
  // which additionally proves the click-through-honest-placeholder invariant -- out of
  // scope for this arc's own moment of value, the business flow + dashboard rollup.)
  await goToInvoices(page)
  await openInvoiceRow(page, 'INV-UI-MIX-VIOLATE')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('INV-UI-MIX-VIOLATE')
  const violationsTable = page.getByTestId('violations-table')
  await expect(violationsTable).toContainText('vat-standard-rate')

  // 5. Fix VAT inline (the only broken field here -- vat-standard-rate is the sole
  // violation) and save. Scoped to the edit-invoice form via xpath sibling lookup (the
  // form carries no per-field test ids -- same idiom as the "detail surface" test above).
  await page.getByTestId('edit-toggle').click()
  const form = page.getByTestId('edit-invoice')
  await form.locator('xpath=.//div[normalize-space(text())="VAT"]/following-sibling::input').fill('75')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByTestId('stale-verdict')).toBeVisible()
  await expect(form, 'a successful save unmounts the editor').toHaveCount(0)

  // 6. Re-validate to green. handleRevalidate also refreshes the status-history timeline
  // in place (history.run(), alongside detail.run()) -- asserting its settled row count
  // (1 genesis row from import + 1 draft->validated promotion row from this revalidate)
  // proves BOTH in-flight fetches this click kicked off have resolved before the next
  // step navigates away and unmounts this view.
  await page.getByTestId('revalidate').click()
  await expect(violationsTable).toContainText('Passes all rules')
  await expect(page.getByTestId('invoice-status-badge')).toContainText('VALIDATED')
  await expect(page.getByTestId('status-history-row')).toHaveCount(2)

  // 7a. Dashboard rollup ready state (Gap 1). [dashboard-scope-per-client] means this
  // page now shows the ACTIVE entity's OWN scoped total, not the tenant-wide count that
  // every other spec in this run adds to ([dashboard-ready-not-counted] is retired by that same
  // change) -- so the exact value is technically knowable here (2: mix-clean plus the
  // now-fixed mix-violate), but this stays existence/ready-only rather than coupling this
  // arc's business-flow assertion to buildMixedCsv's exact row count: only the overview
  // label + a rendered "<N> TOTAL" donut total are asserted, never a specific N.
  await page.getByRole('button', { name: /Overview/ }).click()
  await expect(page.getByText('COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()
  await expect(page.getByText(/^\d+ TOTAL$/)).toBeVisible()

  // 7b. Post-fix Clients health pill -- the deterministic per-entity rollup-updated
  // oracle ([rollup-oracle-per-entity]): both of this fresh entity's invoices are now
  // validated with zero violations, so needs_attention must have dropped to 0. Reusing
  // the SAME `clientRow` locator (Playwright locators re-resolve against the live DOM,
  // not a stale snapshot) -- `toContainText` retries while the fresh ClientsView mount's
  // rollup refetch settles.
  await page.getByRole('button', { name: /Clients/ }).click()
  // Zero survives approvals: this flow now arms an open run on validate (governed tenant),
  // but a validated invoice with an open run is awaiting_approval's population, never
  // needs_attention's (TestStoreRollup_ApprovalRejectedArmIsDraftOnly).
  await expect(clientRow, 'fresh entity health pill must flip to ALL CLEAR once its only violation is fixed').toContainText('ALL CLEAR')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// M5-09-08 (task-256): the M5 milestone gate itself -- both demonstrations Core AC #5
// requires, entirely from the browser, extending this capability flow rather than adding
// a dated spec ([capability-not-date], docs/e2e-convention.md; Stage-1 correction C12).
// Both submits below still route through the list's batch-select-and-submit path
// (invoice-select + batch-submit, M5-09-06), reused for BOTH the first submit and the
// resubmit leg -- these two tests exercise the register's own path specifically, not
// because the detail page lacks a Submit control (it has one; see "detail surface: submit
// one invoice from its own page").
//
// Row 1-3 of the story's Test Specs table ("batch-select and submit", "badge advances",
// "shows a real IRN and rendered QR") are folded into ONE test below (Stage-1 correction
// C13): row 2 says "continuing from the step above" and has no fixture of its own, and
// this file uses no test.describe.serial / module state that would let three independent
// test()s share a fixture -- each test() gets a fresh page and a fresh error collector.

test('submission surface: batch-select and submit a validated invoice, badge advances to ACCEPTED, and its detail shows a real IRN and a rendered QR', async ({
  page,
}, testInfo) => {
  // Cold-fleet headroom, matching this file's own Day-60 precedent (line ~326) -- one
  // sign-in, one submit, one poll-driven badge flip and one detail round trip is
  // comfortable well inside the config's 60s default, but not guaranteed on a fleet still
  // warming up.
  test.setTimeout(120_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M509 accept ${Date.now()}`, tin: freshTin() })

  const invoiceNumber = `INV-M509-ACCEPT-${Date.now()}`
  const inv = await createInvoice(token, {
    entity_id: entity.id,
    ...submittableInvoiceFields(invoiceNumber, MOCK_TIN_ACCEPT),
  })
  await validateInvoice(token, inv.id)
  // The firm tenant is governed (this file's own beforeAll) -- validating arms an open
  // approval run, and isRowSelectable disables the checkbox on one. Close it over the side
  // channel before the row is ever selected.
  await approveUntilClosed(inv.id, await firmApproverTokens())

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const row = invoiceRowByNumber(page, invoiceNumber)
  await row.getByTestId('invoice-select').check()
  await submitSelected(page)

  // AC-3: exactly one POST is what waitForResponse above already pinned (the confirm
  // click); the results panel names THIS invoice as queued.
  await expect(page.getByTestId('batch-submit-results')).toContainText(invoiceNumber)
  await expect(page.getByTestId('batch-submit-results')).toContainText('Queued')

  // AC-4: the row's own badge, observed on the LIST -- no page.reload() anywhere in this
  // test. `-0001` converges via the synchronous queued->accepted shortcut
  // (~800ms adapter latency + near-immediate River pickup, store.go's own doc comment),
  // well inside the config's default 15s expect timeout -- no override needed here
  // (Stage-1 correction C4 reserves the explicit 45s timeout for the PENDING path only).
  await expect(row.getByTestId('invoice-status-badge')).toContainText('ACCEPTED')

  // AC-2/AC-6: open the accepted invoice's detail -- reusing the file's own
  // openInvoiceRow helper (invoice_number is unique per run, so the plain exact-text
  // click it uses is unambiguous here).
  await openInvoiceRow(page, invoiceNumber)
  await assertFiscalRecord(page, invoiceNumber)

  // The detail page fills its column at EVERY wide viewport, not just one.
  // BUG-03-05's `maxWidth: 1080` stranded 588px here (32% of the window) and its own
  // check -- `width <= 1082` -- was SATISFIED by that, so it measured the symptom, not
  // the defect. layout.ts measures the leftover band on BOTH sides instead, which is
  // zero only when nothing caps the container. Compared against the scroll container,
  // not the window: the 252px sidebar sits outside it.
  //
  // Swept rather than pinned at 1920 (where it lived until this commit): a cap only
  // strands what the window gives it room to strand, so the widest viewport is the one
  // that exposes it, and 2560 exposes more of it than 1920 does.
  const fit = await assertFillsColumn(
    page,
    page.getByTestId('invoice-detail'),
    page.locator('main.pf-main .pf-scroll'),
    'invoice detail',
  )
  await testInfo.attach('invoice-detail-column-fit.json', {
    body: JSON.stringify({ invoiceNumber, fit }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('submission surface: reject → fix → re-validate → resubmit → accept, entirely from the browser', async ({ page }) => {
  // Longer than every other test in this file: the pending-path resubmit leg alone can
  // take up to ~22s to converge (Stage-1 correction C4), on top of a reject leg, an
  // edit+re-validate round trip and two full submit cycles.
  test.setTimeout(180_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M509 reject ${Date.now()}`, tin: freshTin() })

  // Reject leg: buyer_tin -0002 fires the mock's scripted rejection
  // (Reason{Code:"NGE-4102", Path:"buyer.tin"}).
  const invoiceNumber = `INV-M509-REJECT-${Date.now()}`
  const inv = await createInvoice(token, {
    entity_id: entity.id,
    ...submittableInvoiceFields(invoiceNumber, MOCK_TIN_REJECT),
  })
  await validateInvoice(token, inv.id)
  await approveUntilClosed(inv.id, await firmApproverTokens())

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const row = invoiceRowByNumber(page, invoiceNumber)
  await row.getByTestId('invoice-select').check()
  await submitSelected(page)

  // -0002 also converges synchronously (queued->rejected, same ~800ms shortcut as
  // -0001's accept) -- default expect timeout is comfortable, no override needed.
  await expect(row.getByTestId('invoice-status-badge')).toContainText('REJECTED')

  await openInvoiceRow(page, invoiceNumber)

  // AC-7: rejection-reasons carries NGE-4102, and a field-flag sits on the Buyer TIN
  // input -- rendered as a SIBLING between the label div and the input
  // (InvoiceEditForm.tsx), the same axis this file's "detail surface" test already uses
  // for `following-sibling::input`; `following-sibling::*[@data-testid="field-flag"]`
  // matches the same way.
  const rejectionCard = page.getByTestId('rejection-reasons')
  await expect(rejectionCard).toBeVisible()
  await expect(rejectionCard.getByTestId('rejection-reason-row')).toContainText('NGE-4102')

  await page.getByTestId('edit-toggle').click()
  const form = page.getByTestId('edit-invoice')
  const buyerTinFlag = form.locator('xpath=.//div[normalize-space(text())="Buyer TIN"]/following-sibling::*[@data-testid="field-flag"]')
  await expect(buyerTinFlag).toBeVisible()
  await expect(buyerTinFlag).toContainText('NGE-4102')

  // The fix: retarget buyer_tin at the PENDING trigger, not back at -0001 -- Stage-1
  // correction C1. -0001/-0002 both converge synchronously, so a resubmit through either
  // would mount its detail already terminal, shouldPollInvoice would be false from the
  // first render, no tick would ever fire, and the history-row assertion below would pass
  // vacuously (confirming M5-09-07's own finding K). -0003 passes buyer-tin-format (the
  // ONLY buyer-TIN rule -- no Luhn rule exists), so it re-validates clean.
  await form.locator('xpath=.//div[normalize-space(text())="Buyer TIN"]/following-sibling::input').fill(MOCK_TIN_PENDING)
  await page.getByRole('button', { name: 'Save changes' }).click()

  // Editing a rejected invoice demotes it to draft (store.go's second recovery edge) --
  // and the rejection card is retained (shouldShowRejectionCard excludes only
  // `accepted`), though its heading flips from the current-verdict wording to the
  // historical one (rejectionProvenance, Stage-1 correction C14) -- asserted here on the
  // row's content, never the heading.
  await expect(page.getByTestId('stale-verdict')).toBeVisible()
  await expect(page.getByTestId('invoice-status-badge')).toContainText('DRAFT')
  await expect(rejectionCard).toBeVisible()
  await expect(rejectionCard.getByTestId('rejection-reason-row')).toContainText('NGE-4102')

  await page.getByTestId('revalidate').click()
  await expect(page.getByTestId('invoice-status-badge')).toContainText('VALIDATED')

  // AC-10: the transmit GATE is already clear here -- TransmitClearTx tests
  // EXISTS(state='approved') over ALL runs, and the first leg's approval above already
  // satisfies that. This approval is for isRowSelectable instead, which reads only the
  // LATEST run's state, and the revalidate above just cancelled that run and armed a fresh
  // open one (revalidate.go/store.go demotion path). Without this, the checkbox below stays
  // disabled even though the server would happily accept the resubmit.
  await approveUntilClosed(inv.id, await firmApproverTokens())

  // Resubmit leg: back to the list -- this test still resubmits through the register's
  // batch-select-and-submit path (AC-3), not the detail page's own Submit control.
  await page.getByRole('button', { name: '← All invoices' }).click()
  await expect(row).toBeVisible()
  await row.getByTestId('invoice-select').check()
  await submitSelected(page)

  // Non-vacuity proof (Stage-1 correction C1), in this EXACT order, with no navigation
  // after this row click: the pending trigger holds `submitted` for >=10s, so pinning the
  // detail on that in-flight state, capturing the baseline history count, THEN watching
  // the badge flip to ACCEPTED and a 9th row appear is what proves the tick actually
  // drove both -- not a count read at an arbitrary, possibly-already-terminal moment.
  await openInvoiceRow(page, invoiceNumber)
  const badge = page.getByTestId('invoice-status-badge')
  const historyRows = page.getByTestId('status-history-row')

  // 1: Created·draft, 2: draft->validated, 3: validated->queued, 4: queued->rejected,
  // 5: rejected->draft (edit), 6: draft->validated (revalidate), 7: validated->queued
  // (resubmit), 8: queued->submitted -- deterministic given this exact fixture lifecycle.
  await expect(badge).toContainText('SUBMITTED')
  await expect(historyRows).toHaveCount(8)

  // Stage-1 correction C4: the ONLY assertion in this file that needs an explicit
  // timeout above the config's 15s default -- pending convergence (adapter latency + two
  // poll hops + River's own 5s scheduler interval per hop) can run ~11-22s worst case.
  // No `page.waitForTimeout` anywhere in this test -- only retrying assertions.
  await expect(badge).toContainText('ACCEPTED', { timeout: 45_000 })
  // 9: submitted->accepted -- can ONLY have arrived via the tick's own
  // shouldRefreshHistory -> history.run(), never a user action (none happened between the
  // two badge assertions above) -- this IS M5-09-07's live-refresh oracle.
  await expect(historyRows).toHaveCount(9)

  // AC-6, on this SAME mounted view -- no re-navigation needed (Stage-1 correction C1):
  // the fiscal record renders once the overlay flips the invoice to accepted.
  await assertFiscalRecord(page, invoiceNumber)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// INVED-01-08, Core AC 6 -- the headline journey: a rejected invoice, edited back to
// draft with its rejection reasons RETAINED, then re-validated to green, entirely from the
// detail page. Deliberately NOT folded into the reject/resubmit test above: that test is
// the M5-09 gate with a delicate 8/9-row history sequence and a poll-timing dependency
// (the PENDING-trigger resubmit leg); coupling this story's headline journey to it would
// make both harder to diagnose on a failure.
//
// Fixture: forcing `rejected` via the transitions API does NOT write rejection_reasons
// (transitionTx only mutates that column to CLEAR it, and only on target===accepted) --
// shouldShowRejectionCard requires rejection_reasons.length > 0, so that path would leave
// the card untestable. The only source of a REAL reason is the mock APP, so this reuses
// the reject/resubmit test's own proven recipe verbatim: validate, then batch-submit
// through the UI so the mock adapter actually scripts the rejection.
test('detail surface: a rejected invoice is edited back to draft with its reasons retained, then re-validated to green (Core AC 6)', async ({
  page,
}) => {
  test.setTimeout(120_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVED-01 loop ${Date.now()}`, tin: freshTin() })

  const invoiceNumber = `INV-INVED01-LOOP-${Date.now()}`
  const inv = await createInvoice(token, {
    entity_id: entity.id,
    ...submittableInvoiceFields(invoiceNumber, MOCK_TIN_REJECT),
  })
  await validateInvoice(token, inv.id)
  await approveUntilClosed(inv.id, await firmApproverTokens())

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const row = invoiceRowByNumber(page, invoiceNumber)
  await row.getByTestId('invoice-select').check()
  await submitSelected(page)
  // -0002 converges synchronously (queued->rejected, ~800ms adapter latency) -- default
  // expect timeout is comfortable, no override needed (this file's own established note).
  await expect(row.getByTestId('invoice-status-badge')).toContainText('REJECTED')

  await openInvoiceRow(page, invoiceNumber)

  // The rejection card carries the mock's real reason, and history sits at 4 rows: 1
  // genesis, 2 draft->validated, 3 validated->queued, 4 queued->rejected.
  const rejectionCard = page.getByTestId('rejection-reasons')
  await expect(rejectionCard).toBeVisible()
  await expect(rejectionCard.getByTestId('rejection-reason-row')).toContainText('NGE-4102')
  await expect(page.getByTestId('status-history-row')).toHaveCount(4)

  // Edit -> change ONLY a line's description. diffEditInput therefore emits {} for the
  // header and diffLineItems emits the one changed line, so only `line_items` reaches the
  // wire -- Core AC 2's isolated line-only PATCH path.
  await page.getByTestId('edit-toggle').click()
  await page.getByTestId('line-row').nth(0).locator('input').first().fill('Widget A revised')
  await page.getByRole('button', { name: 'Save changes' }).click()

  // The demotion: rejected -> draft retains the reasons (transitionTx clears
  // rejection_reasons only on target===accepted, [reason-lifecycle]) and stamps a 5th
  // history row.
  await expect(page.getByTestId('stale-verdict')).toBeVisible()
  await expect(page.getByTestId('edit-invoice')).toHaveCount(0)
  await expect(page.getByTestId('invoice-status-badge')).toContainText('DRAFT')
  await expect(rejectionCard).toBeVisible()
  await expect(rejectionCard.getByTestId('rejection-reason-row')).toContainText('NGE-4102')
  await expect(page.getByTestId('status-history-row')).toHaveCount(5)
  await expect(page.getByTestId('status-history-row').last()).toContainText('rejected → draft')

  // Re-validate is enabled again ([revalidate-visibility]/AC #2) -- the fixture's numbers
  // were always clean (only the description text changed), so this re-validates green.
  const revalidate = page.getByTestId('revalidate')
  await expect(revalidate).toBeEnabled()
  await revalidate.click()
  await expect(page.getByTestId('violations-table')).toContainText('Passes all rules')
  await expect(page.getByTestId('invoice-status-badge')).toContainText('VALIDATED')
  await expect(page.getByTestId('status-history-row')).toHaveCount(6)
  await expect(page.getByTestId('status-history-row').last()).toContainText('draft → validated')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// INVED-01-08, Core AC 5 -- the draft line editor: add / remove / cancel, and the passive
// computed-sum hint tracking the LIVE edited rows rather than the last-saved
// inv.line_items (the documented trap, InvoiceDetail.tsx's own doc comment on lineSum).
// Read-only rows carry no testid (InvoiceDetail.tsx), so post-save count/order is asserted
// by RE-OPENING Edit and reading the seeded inputs, plus description text on the read-only
// body.
test('detail surface: the draft line editor -- add, remove, cancel, and a live computed-sum hint (Core AC 5)', async ({
  page,
}) => {
  test.setTimeout(90_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVED-01 lines ${Date.now()}`, tin: freshTin() })

  const invoiceNumber = `INV-INVED01-LINES-${Date.now()}`
  const inv = await createInvoice(token, { entity_id: entity.id, ...lineEditFields(invoiceNumber) })

  // PATCH recorder, registered once `inv.id` is known and before the browser opens --
  // T8's non-vacuity proof that Cancel truly sent nothing, and T5/T6's proof that Save
  // truly sent exactly one request each.
  const patchCalls: string[] = []
  page.on('request', (r) => {
    if (r.method() === 'PATCH' && new URL(r.url()).pathname.endsWith(`/invoices/${inv.id}`)) patchCalls.push(r.url())
  })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  // T7: two lines summing to 900.00 against a 1000 header subtotal (never validated, so no
  // verdict is at risk). The hint must be fed the LIVE edited rows, not inv.line_items --
  // presence alone is vacuous (the node always renders); only a value that MOVES as the
  // operator types proves it.
  await page.getByTestId('edit-toggle').click()
  const lineRows = page.getByTestId('line-row')
  await expect(lineRows).toHaveCount(2)
  await expect(page.getByTestId('computed-line-sum')).toContainText('900.00')
  await lineRows.nth(0).locator('input').nth(2).fill('200')
  await expect(page.getByTestId('computed-line-sum')).toContainText('1300.00')
  await expect(page.getByRole('button', { name: 'Save changes' })).toBeEnabled()

  // T8: cancel with the unit-price edit above still pending, plus an untouched-but-open
  // row-1 description edit -- Cancel must discard BOTH and send nothing.
  await lineRows.nth(1).locator('input').first().fill('Widget B (unsaved)')
  await page.getByTestId('edit-cancel').click()
  await expect(page.getByTestId('edit-invoice')).toHaveCount(0)
  await expect(page.getByTestId('invoice-detail')).toContainText('Widget A')
  await expect(page.getByTestId('invoice-detail')).toContainText('Widget B')
  expect(patchCalls, 'Cancel must issue no PATCH').toHaveLength(0)

  // T5: re-open (re-seeded from the still-unedited server record, since Cancel never
  // refetches), add a third line, save.
  await page.getByTestId('edit-toggle').click()
  await page.getByTestId('line-add').click()
  const newRow = page.getByTestId('line-row').nth(2)
  await newRow.locator('input').nth(0).fill('Widget C')
  await newRow.locator('input').nth(1).fill('2')
  await newRow.locator('input').nth(2).fill('50')
  await newRow.locator('input').nth(3).fill('100')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByTestId('edit-invoice')).toHaveCount(0)
  await expect(page.getByTestId('invoice-detail')).toContainText('Widget C')
  expect(patchCalls, 'retro-non-vacuity for T8: Save really does send exactly one PATCH').toHaveLength(1)

  // T6: re-open (now 3 server-side lines), remove the middle row (Widget B), save -- order
  // must survive: Widget A stays row 0, Widget C shifts up to row 1.
  await page.getByTestId('edit-toggle').click()
  await expect(page.getByTestId('line-row')).toHaveCount(3)
  await page.getByTestId('line-row').nth(1).getByTestId('line-remove').click()
  await page.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByTestId('edit-invoice')).toHaveCount(0)
  await expect(page.getByTestId('invoice-detail')).not.toContainText('Widget B')

  // Re-open once more to assert order/count through the seeded inputs (read-only rows
  // carry no testid).
  await page.getByTestId('edit-toggle').click()
  const finalRows = page.getByTestId('line-row')
  await expect(finalRows).toHaveCount(2)
  await expect(finalRows.nth(0).locator('input').first()).toHaveValue('Widget A')
  await expect(finalRows.nth(1).locator('input').first()).toHaveValue('Widget C')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('submission surface: a failed invoice is an honest dead end', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M509 failed ${Date.now()}`, tin: freshTin() })

  // Stage-1 correction C3: the `-0006` timeout trigger is FORBIDDEN, not a fallback -- it
  // returns Retryable under River's unmodified attempt^4 policy, ~78 minutes before
  // MarkFailed, which both blows this test's budget and parks a `queued` row that would
  // turn list polling on for every later topology spec against this same PR environment.
  // Both edges below are legal (validated->queued, queued->failed) and TransitionHandler
  // refuses only `target === validated` -- no submission job is ever created, so this
  // fixture is terminal before the browser even opens.
  const invoiceNumber = `INV-M509-FAILED-${Date.now()}`
  const inv = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })
  await validateInvoice(token, inv.id)
  // TransmitClearTx gates this transition server-side too (EXISTS(state='approved')) -- the
  // governed tenant's open run must close before the queued edge is legal here.
  await approveUntilClosed(inv.id, await firmApproverTokens())
  await transitionInvoice(token, inv.id, 'queued')
  await transitionInvoice(token, inv.id, 'failed')

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const row = invoiceRowByNumber(page, invoiceNumber)

  // AC-8: a failed row can never be batch-selected. isRowSelectable is `validated` AND no
  // open approval run (APPR-08-09); a `failed` row fails the status half, so the second
  // half never comes into it here.
  await expect(row.getByTestId('invoice-select')).toBeDisabled()

  await openInvoiceRow(page, invoiceNumber)

  await expect(page.getByTestId('failed-dead-end')).toBeVisible()
  await expect(page.getByTestId('failed-dead-end')).toContainText('cannot be re-driven')
  // [failed-no-reason-lands-on-the-detail] (task-332, BUG-01-06 / BUG-06-06): this
  // invoice is API-transitioned straight to failed, so failure_kind is null -- the panel
  // renders the fallback explanation, not a silent gap. BUG-06-07 (task-389) gates exactly
  // one recorded kind end-to-end below (acknowledged_no_verdict, off seeded DEMO-2026-1004)
  // -- e2e cannot import the SPA's copy constants (incompatible tsconfigs), so the other
  // two kinds and the legacy-NULL case are proven by the Go/SPA unit suites plus the seed
  // and the Phase 3.5 deploy-gate checklist, not by a browser assertion here.
  await expect(page.getByTestId('failure-detail')).toContainText('was not recorded')
  // `can_edit` is false for a failed invoice ([gates-on-the-wire], store.go's
  // canEdit/canTransition), so the `can_edit`-gated actions bar (Edit, Re-validate, and
  // Submit together) renders nothing -- `edit-invoice` would be vacuous here (Edit is never
  // clicked), so `invoice-actions`/`edit-toggle` are the real guard ([actions-visibility]).
  // No button matches /submit/i either: the detail page's own Submit is gated by that same
  // `can_edit` check, and the register's Submit button is unmounted while on the detail
  // view -- App.tsx's view switch is exclusive, never both mounted at once. The Approve/
  // Reject pair (task-554, APPR-13-04) is gated on `!editing` alone, not `can_edit`, so it
  // still renders here -- outside this bar, per-status disabled state covered by the SPA
  // unit suite, not asserted here.
  await expect(page.getByTestId('revalidate')).toHaveCount(0)
  await expect(page.getByTestId('invoice-actions')).toHaveCount(0)
  await expect(page.getByTestId('edit-toggle')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /submit/i })).toHaveCount(0)

  // BUG-04-07 (story AC1): the UBL control sits OUTSIDE that bar
  // ([ubl-button-outside-invoice-actions]) because can_view_ubl tracks CONTENT, not
  // lifecycle -- and `failed` is exactly where a compliance user needs the document most.
  // Free-riding on this fixture: cleanInvoiceFields is UBL-complete, and the line above
  // already proves the bar is gone, so no standalone test could assert anything stronger.
  await expect(page.getByTestId('view-ubl')).toBeVisible()
  await expect(page.getByTestId('view-ubl')).toBeEnabled()
  await expect(page.getByTestId('view-ubl-blocked-reason')).toHaveCount(0)

  // resolve-outside is the one permitted control on this dead-end card -- present
  // alongside the removed actions bar, not inside it.
  await expect(page.getByTestId('resolve-outside')).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('resolve/unresolve loop: marking a failed invoice resolved drops it from needs-attention without re-driving it, and undo reverses that', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M509 resolve ${Date.now()}`, tin: freshTin() })

  // Same sanctioned failed-fixture chain as the dead-end test above -- the -0006
  // timeout trigger is forbidden there for the same reason it would be here.
  const invoiceNumber = `INV-M509-RESOLVE-${Date.now()}`
  const inv = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })
  await validateInvoice(token, inv.id)
  // Same server-side gate as the dead-end test above (TransmitClearTx).
  await approveUntilClosed(inv.id, await firmApproverTokens())
  await transitionInvoice(token, inv.id, 'queued')
  await transitionInvoice(token, inv.id, 'failed')

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  const reason = `resolved outside the system ${Date.now()}`
  await page.getByTestId('resolve-outside-reason').fill(reason)
  await page.getByTestId('resolve-outside').click()

  // Core AC #5: the banner carries the operator's own reason. Core AC #6: status is
  // untouched -- resolving is not a transition.
  await expect(page.getByTestId('detail-resolved-banner')).toContainText(reason)
  await expect(page.getByTestId('invoice-status-badge')).toContainText('FAILED')

  const row = invoiceRowByNumber(page, invoiceNumber)

  // Needs attention ON: kept_as_is_at IS NOT NULL takes it out of the server-side
  // predicate (store.go's NeedsAttention fragment) -- it must vanish from the DOM.
  await goToInvoices(page)
  const filteredResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('needs_attention') === 'true',
  )
  await page.getByTestId('needs-attention-toggle').click()
  await filteredResp
  await expect(row).toHaveCount(0)

  // Needs attention OFF: the row is still there (nothing was deleted), now carrying
  // the resolved marker (Core AC #3).
  const unfilteredResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('needs_attention') === null,
  )
  await page.getByTestId('needs-attention-toggle').click()
  await unfilteredResp
  await expect(row).toBeVisible()
  await expect(row.getByTestId('invoice-resolved-marker')).toBeVisible()

  // Undo, then re-apply the filter: the round trip reverses cleanly (Core AC #5).
  await openInvoiceRow(page, invoiceNumber)
  await page.getByTestId('resolve-outside-undo').click()
  await expect(page.getByTestId('resolve-outside-reason')).toBeVisible()
  await expect(page.getByTestId('invoice-status-badge')).toContainText('FAILED')

  await goToInvoices(page)
  const refilteredResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('needs_attention') === 'true',
  )
  await page.getByTestId('needs-attention-toggle').click()
  await refilteredResp
  await expect(row).toBeVisible()
  await expect(row.getByTestId('invoice-resolved-marker')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// BUG-06-07 (task-389): the test above proves the panel renders for failure_kind IS NULL
// (absence of a value). Nothing proved a NON-null kind survives DB -> invoiceColumns/
// scanInvoice -> the Invoice struct -> the SPA's InvoiceRecord type -> failureExplanation()
// together, on a real deployed environment -- four links never exercised as one. This reads
// a seeded row (DEMO-2026-1004, db/seed.dev.sql), not an API-created fixture: it's the one
// way to prove a kind stamped by real code (BUG-06-02/03) round-trips the whole stack, since
// this suite's own fixtures reach `failed` only through POST /transitions, which is always
// null (see the test above). 'acknowledged_no_verdict' is the kind gated here -- it carries
// the operationally critical double-filing warning ("twice"), the single word BUG-06-05's
// own vitest suite (invoices.test.ts) also anchors on, so this assertion and that unit test
// move together instead of drifting apart. The other two kinds (payload_not_built,
// never_acknowledged) and the legacy-NULL case are proven by the Go/SPA unit suites plus the
// seed + the Phase 3.5 deploy-gate checklist -- not a second/third/fourth copy of this test,
// which docs/e2e-convention.md names as the failure mode to avoid.
test('submission surface: a failed invoice with a recorded kind explains itself', async ({ page }) => {
  const errors = collectErrors(page)

  await signInFirm(page)
  // shortName() (frontend/app/src/lib/clients.ts) strips a trailing " Ltd"/"Limited"/"Plc"
  // before the switcher renders it -- selectEntity() matches on that rendered text, so the
  // literal business_entities.name ('Adeyemi & Sons Trading Ltd') would time out.
  await selectEntity(page, 'Adeyemi & Sons Trading')
  await goToInvoices(page)
  await openInvoiceRow(page, 'DEMO-2026-1004')

  await expect(page.getByTestId('failed-dead-end')).toBeVisible()
  await expect(page.getByTestId('failure-detail')).toContainText(/twice/i)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The founder-reversed visibility model: Submit always renders inside the actions bar now,
// disabled+reasoned when `can_submit` is false, rather than hidden. This test proves that on
// a draft first, then drives a real submit-and-verdict journey entirely from the detail
// page's own Submit control -- no batch-select, no navigation away.
test('detail surface: submit one invoice from its own page -- cancel sends nothing, confirm sends one, and the verdict lands without leaving', async ({
  page,
}) => {
  // Cold-fleet headroom (this file's own Day-60 precedent) plus the pending-trigger's
  // >=10s hold before the poll worker resolves it to accepted.
  test.setTimeout(120_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `INVED02 submit ${Date.now()}`, tin: freshTin() })

  // Never validated, so can_submit stays false for the disabled-state leg below.
  const draftNumber = `INV-INVED02-DRAFT-${Date.now()}`
  await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(draftNumber) })

  // PENDING trigger (mockPendingPolls=2 x mockPollAfterSeconds=5s, mock_adapter.go): holds
  // `submitted` for >=10s, the window the history-row-growth assertion below needs to be
  // non-vacuous -- the same fixture choice as the reject/resubmit test's own resubmit leg.
  const invoiceNumber = `INV-INVED02-SUBMIT-${Date.now()}`
  const inv = await createInvoice(token, {
    entity_id: entity.id,
    ...submittableInvoiceFields(invoiceNumber, MOCK_TIN_PENDING),
  })
  await validateInvoice(token, inv.id)

  // Request counter, not just the DOM: the only reliable proof Cancel sends nothing, and
  // that Confirm sends exactly one POST carrying exactly this invoice's id ([no-bulk-on-detail]).
  const submitPosts: Request[] = []
  page.on('request', (r) => {
    if (r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices/submissions')) submitPosts.push(r)
  })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  // Disabled-with-reason leg, on the draft fixture: Submit is visible but unclickable, and
  // the backend's own sentence is on screen ([revalidate-visibility] convention, extended to
  // Submit).
  await openInvoiceRow(page, draftNumber)
  // The approval-trail card fetches its run unconditionally on mount (InvoiceDetail.tsx:
  // 196-199), so this never-validated draft still renders the empty branch -- topology's
  // only remaining assertion of it now that a validated firm invoice always arms a run.
  await expect(page.getByTestId('approval-trail-empty')).toBeVisible()
  await expect(page.getByTestId('detail-submit')).toBeVisible()
  await expect(page.getByTestId('detail-submit')).toBeDisabled()
  await expect(page.getByTestId('submit-blocked-reason')).toContainText(
    'Only validated invoices can be submitted — re-validate this invoice first.',
  )

  await page.getByRole('button', { name: '← All invoices' }).click()
  await openInvoiceRow(page, invoiceNumber)
  await expect(page.getByTestId('invoices-list')).toHaveCount(0)

  const submitBtn = page.getByTestId('detail-submit')
  await expect(submitBtn).toBeVisible()
  // AC-4: the undecided state, observed in a browser for the first time -- disabled, with
  // the server's own reason RENDERED (submit-blocked-reason is a sibling text node, not a
  // `title` attribute -- a title-only reason is invisible in Chromium and has bitten this
  // suite before).
  await expect(submitBtn).toBeDisabled()
  await expect(page.getByTestId('submit-blocked-reason')).toContainText(AWAITING_APPROVAL_REASON)

  // Approve over the side channel, then force a refetch: a validated invoice does not poll
  // (shouldPollList needs queued/submitted), so nothing on screen would notice otherwise.
  // Reuses the same back-then-reopen round trip the draft leg above already needed.
  await approveUntilClosed(inv.id, await firmApproverTokens())
  await page.getByRole('button', { name: '← All invoices' }).click()
  await openInvoiceRow(page, invoiceNumber)
  await expect(page.getByTestId('invoices-list')).toHaveCount(0)
  await expect(submitBtn).toBeEnabled()

  await submitBtn.click()
  const prompt = page.getByTestId('detail-submit-confirm-prompt')
  await expect(prompt).toContainText('Send this invoice for transmission?')
  await expect(prompt).toContainText('Nothing here can pull it back.')

  // Cancel is the identity transition ([no-bulk-on-detail], same reducer as the bulk bar) --
  // no arm survives, so no request fires.
  await page.getByTestId('detail-submit-cancel').click()
  expect(submitPosts, 'Cancel must send no submission POST').toHaveLength(0)
  await expect(page.getByTestId('invoice-status-badge')).toContainText('VALIDATED')
  await expect(submitBtn).toBeVisible()

  // Re-arm and confirm: exactly one POST, carrying exactly this one invoice id.
  await submitBtn.click()
  await expect(prompt).toBeVisible()
  await page.getByTestId('detail-submit-confirm').click()
  await expect.poll(() => submitPosts.length).toBe(1)
  const submittedIds = (submitPosts[0].postDataJSON() as { invoice_ids: string[] }).invoice_ids
  expect(submittedIds, '[no-bulk-on-detail]: exactly one id, this invoice only').toHaveLength(1)
  expect(submittedIds[0]).toBe(inv.id)

  // No navigation anywhere below -- the verdict lands on this same mounted view.
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  await expect(page.getByTestId('invoices-list')).toHaveCount(0)

  const badge = page.getByTestId('invoice-status-badge')
  const historyRows = page.getByTestId('status-history-row')

  // 1: Created·draft, 2: draft->validated, 3: validated->queued, 4: queued->submitted --
  // deterministic given this exact fixture lifecycle.
  await expect(badge).toContainText('SUBMITTED')
  await expect(historyRows).toHaveCount(4)

  // Pending-convergence precedent (this file's reject/resubmit test): the ONLY assertion
  // needing a timeout above the config's 15s default.
  await expect(badge).toContainText('ACCEPTED', { timeout: 45_000 })
  await expect(page.getByTestId('invoices-list')).toHaveCount(0)
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
  // 5: submitted->accepted -- can ONLY have arrived via the poll tick's own
  // shouldRefreshHistory -> history.run(), never a user action (none happened between the
  // two badge assertions above).
  await expect(historyRows).toHaveCount(5)

  // AUDIT-02-07 (AC-3): the worker writes actor 'system' itself (internal/invoice/actor.go
  // SystemActor), so this run's OWN submitted->accepted row is the oracle -- no seeded row,
  // no extra navigation. toHaveText is case-sensitive, so the raw rung's lowercase 'system'
  // cannot pass it; the class check is the AC's "in mono" half, which the text alone cannot
  // see (lib/actor.ts narrows an unknown actor_kind to raw, keeping the text and adding mono).
  const systemActor = page.getByTestId('status-history-actor').last()
  await expect(systemActor).toHaveText('System')
  await expect(systemActor).not.toHaveClass(/mono/)

  await assertFiscalRecord(page, invoiceNumber)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// INVCR-01-16 (task-292) AC-3 -- Core AC 1's ordering claim ("no success copy before the
// response") made OBSERVABLE rather than merely asserted. CreateForm.tsx's own header
// comment states the contract: "NOTHING here may affirm a filing. There is no success
// banner, no tick, no optimistic row: the affirmation is the real invoice detail screen
// rendering the server's own row, reached only after the 201." An unthrottled assertion
// of "no success copy yet" would almost always pass VACUOUSLY on a real deployed round
// trip (the 201 can easily have already landed by the time Playwright looks) --
// page.route delays the create POST by a fixed window so the pre-response gap is real,
// matching this subtask's own "must not race" constraint (§14 #5).
test('INVCR-E2E-3 firm: manual entry persists and affirms nothing before the response', async ({ page }) => {
  const errors = collectErrors(page)

  await signInFirm(page)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.getByRole('button', { name: 'Skip — enter manually' }).click()

  const invoiceNumber = `INV-E2E-MANUAL-${Date.now()}`
  await page.getByPlaceholder('INV-0000-00000').fill(invoiceNumber)

  // Delay ONLY the create POST -- nothing else on this screen requests this same path.
  await page.route('**/api/invoice/v1/invoices', async (route) => {
    if (route.request().method() === 'POST') await new Promise((r) => setTimeout(r, 2_000))
    await route.continue()
  })

  const createResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices'),
    { timeout: 30_000 },
  )
  const fileBtn = page.getByRole('button', { name: 'File invoice' })
  await expect(fileBtn).toBeEnabled()
  await fileBtn.click()

  // The 2s route delay makes this window real, not a race: fileDraftInvoice
  // (lib/invoiceDraft.ts) sets the in-flight flag SYNCHRONOUSLY before its one `await`,
  // so "Filing…" renders immediately, while nothing that could only come from the 201 --
  // the real detail screen -- exists yet.
  await expect(page.getByRole('button', { name: 'Filing…' }), 'the in-flight state renders immediately on click').toBeVisible()
  await expect(page.getByTestId('invoice-detail'), 'no success copy before the response').toHaveCount(0)
  await expect(page.getByRole('heading', { level: 1 })).toHaveCount(0)

  await createResp
  await expect(page.getByTestId('invoice-detail'), 'the real detail renders once the 201 lands').toBeVisible({ timeout: 30_000 })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(invoiceNumber)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// task-329 (BUG-01-03): the register's own pagination + page-scoped selection. Like every
// other test() in this file, each builds its OWN fixture (no test.describe.serial / shared
// module state -- see the M5-09-08 comment above for why).
//
// db/seed.dev.sql caps every curated entity under 51 rows (its own documented ceiling), so a
// >50-row page can only come from a fixture built here, on a fresh entity -- paging through a
// curated one would rot the page-1 specs earlier in this file. 51 sequential createInvoice
// round trips against Railway is real wall-clock cost nothing else here pays, so each fixture
// below is 1 sequential "anchor" invoice, awaited alone, then N invoices via Promise.all.
// The anchor is always the OLDEST row (`ORDER BY created_at DESC, id DESC`, store.go), so it
// always sorts onto page 2 regardless of how the concurrent batch ties against itself --
// which row lands on page 2 is then known ahead of time without 51 sequential awaits.
// anchorOverrides (task-331, BUG-01-05): mirrors submittableInvoiceFields' own override
// pattern above -- cleanInvoiceFields' anchor gets a fixed buyer_tin ('87654321-0002'),
// which the register-search test can't use since it needs a TIN unique to the anchor
// alone. Optional and additive: both existing callers omit it and are unaffected.
async function buildAnchoredPage(
  token: string,
  entityId: string,
  bulkCount: number,
  anchorOverrides?: Partial<ReturnType<typeof cleanInvoiceFields>>,
): Promise<{ anchorNumber: string; bulk: Awaited<ReturnType<typeof createInvoice>>[] }> {
  const anchorNumber = `INV-BUG0103-ANCHOR-${Date.now()}`
  await createInvoice(token, { entity_id: entityId, ...cleanInvoiceFields(anchorNumber), ...anchorOverrides })
  const bulk = await Promise.all(
    Array.from({ length: bulkCount }, (_, i) =>
      createInvoice(token, { entity_id: entityId, ...cleanInvoiceFields(`INV-BUG0103-BULK-${i}-${Date.now()}`) }),
    ),
  )
  return { anchorNumber, bulk }
}

test('register-pagination: the list discloses its true total and the last page is reachable', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-01-03 page ${Date.now()}`, tin: freshTin() })
  const { anchorNumber } = await buildAnchoredPage(token, entity.id, 50)

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const pager = page.getByTestId('invoices-pager')
  await expect(pager).toBeVisible()
  await expect(pager).toContainText('SHOWING 1–50 OF 51')
  await expect(pager).toContainText('PAGE 1 / 2')
  await expect(page.getByTestId('invoice-row').filter({ hasText: anchorNumber }), 'the anchor row is the oldest, so it must not be on page 1').toHaveCount(0)

  const page2Resp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('offset') === '50',
  )
  await pager.getByRole('button', { name: 'Next →' }).click()
  await page2Resp

  await expect(pager).toContainText('SHOWING 51–51 OF 51')
  await expect(pager).toContainText('PAGE 2 / 2')
  await expect(page.getByTestId('invoice-row').filter({ hasText: anchorNumber }), 'page 2 must render the row that was not reachable on page 1').toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('register-selection: select-all is page-scoped and paging clears it', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-01-03 select ${Date.now()}`, tin: freshTin() })
  // The anchor is never validated -- it always lands on page 2 (buildAnchoredPage), so
  // page 2's own selectable-row count is not this test's concern.
  const { bulk } = await buildAnchoredPage(token, entity.id, 50)

  // All 50 `bulk` rows are strictly newer than the anchor, so ALL of them land on page 1
  // regardless of how they tie-break against each other -- validating exactly this many
  // makes page 1's select-all count known ahead of time without depending on row order.
  const SELECTABLE_COUNT = 12
  await Promise.all(bulk.slice(0, SELECTABLE_COUNT).map((inv) => validateInvoice(token, inv.id)))
  // AC-2: this test submits NOTHING -- it fails purely on SELECTABILITY, not submission.
  // Every one of these 12 now arms an open run on validate (governed tenant); close them
  // all before select-all or the summary bar unmounts entirely at zero selectable.
  const approverTokens = await firmApproverTokens()
  await Promise.all(bulk.slice(0, SELECTABLE_COUNT).map((inv) => approveUntilClosed(inv.id, approverTokens)))

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  await expect(page.getByTestId('invoices-pager')).toBeVisible()
  await page.getByTestId('invoice-select-all').click()
  await expect(page.getByTestId('batch-submit-summary'), 'select-all must select only this page\'s selectable rows').toContainText(`${SELECTABLE_COUNT} selected on this page`)

  const page2Resp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('offset') === '50',
  )
  await page.getByTestId('invoices-pager').getByRole('button', { name: 'Next →' }).click()
  await page2Resp

  await expect(page.getByTestId('batch-submit-summary'), 'paging must clear the selection').toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// APPR-16-08 (task-540): the confirm stage AC-3 gave InvoicesList's batch-submit button
// (Core AC-8/AC-1/AC-2 of APPR-16). Two invoices, not one: `bar.visible = n > 0`
// (reviewBatch.ts) unmounts batch-submit-summary entirely at n=0, so a single-row
// toggle-off couldn't show the idle rung this test needs to see.
//
// Non-vacuity (Stage-1 correction C-3, load-bearing): InvoicesList.tsx:195-204 resets
// `phase` to 'idle' on every `rows` identity change, and the 2s live-refresh poll
// produces a fresh `rows` every tick WHILE any row is in-flight (shouldPollList). Both
// fixture invoices stay `validated` throughout arm/re-arm, so polling never turns on --
// this is what makes the disarm below attributable to the selection change and not a
// poll tick. The wait-then-assert below proves that directly rather than assuming it.
test('register-confirm-stage: arm, a selection change disarms, re-arm sends exactly one POST', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `APPR-16-08 confirm ${Date.now()}`, tin: freshTin() })

  const num1 = `INV-APPR1608-A-${Date.now()}`
  const num2 = `INV-APPR1608-B-${Date.now()}`
  const [inv1, inv2] = await Promise.all([
    createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(num1) }),
    createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(num2) }),
  ])
  await Promise.all([validateInvoice(token, inv1.id), validateInvoice(token, inv2.id)])
  // Both rows are checked below (inv2 only to prove the uncheck disarms), so both must be
  // selectable -- close the open run each validate just armed.
  const approverTokens = await firmApproverTokens()
  await Promise.all([approveUntilClosed(inv1.id, approverTokens), approveUntilClosed(inv2.id, approverTokens)])

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const row1 = invoiceRowByNumber(page, num1)
  const row2 = invoiceRowByNumber(page, num2)
  await row1.getByTestId('invoice-select').check()
  await row2.getByTestId('invoice-select').check()

  // AC-2: request interception registered BEFORE the arm click -- arming alone must
  // issue no POST to the submissions endpoint. Recorded for the whole test, not just this
  // window, so the later re-arm+confirm can assert exactly one.
  const submissionPosts: Request[] = []
  page.on('request', (req) => {
    if (req.method() === 'POST' && new URL(req.url()).pathname.endsWith('/api/invoice/v1/invoices/submissions')) {
      submissionPosts.push(req)
    }
  })

  await page.getByTestId('batch-submit').click()
  const confirmBtn = page.getByTestId('batch-submit-confirm')
  const cancelBtn = page.getByTestId('batch-submit-cancel')
  await expect(confirmBtn).toBeVisible()
  await expect(cancelBtn).toBeVisible()
  await expect(page.getByTestId('batch-submit')).toHaveCount(0)
  expect(submissionPosts, 'arming alone must issue no POST').toHaveLength(0)

  // AC-3: the bar's two-stage layout, on the deployed build, at every WIDE_WIDTHS entry.
  const bar = page.getByTestId('batch-submit-summary')
  const container = page.getByTestId('invoices-list')

  // #1 + #3 are the same geometric fact read two ways: assertFillsColumn's symmetric-gap
  // check demands near-zero gap on BOTH edges, so one pass proves the bar both fills its
  // column AND shares the table's left edge, at every width.
  const barFit = await assertFillsColumn(page, bar, container, 'batch-submit-summary vs invoices-list')
  await testInfo.attach('batch-submit-bar-column-fit.json', {
    body: JSON.stringify({ entity: entity.name, fit: barFit }, null, 2),
    contentType: 'application/json',
  })

  // #2: containment, not fill -- the confirm block sits somewhere inside the bar, not
  // necessarily flush to its edges, so this reuses gaps() directly rather than
  // assertFillsColumn's tight-slack fill check. n=2 selected at this point, so
  // confirmPrompt is deterministic (reviewBatch.ts's bulkBarView).
  const confirmPrompt = page.getByText('Send 2 invoices for transmission?', { exact: true })
  const entryViewport = page.viewportSize()
  const confirmFit: { width: number; left: number; right: number }[] = []
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const [barBox, confirmBox] = await Promise.all([bar.boundingBox(), confirmPrompt.boundingBox()])
      expect(barBox && confirmBox, `bar and confirm prompt must both render at ${width}px`).toBeTruthy()
      const g = gaps(confirmBox!, barBox!)
      expect(g.left, `confirm block must not start left of the bar at ${width}px`).toBeGreaterThanOrEqual(0)
      expect(g.right, `confirm block must not extend right of the bar at ${width}px`).toBeGreaterThanOrEqual(0)
      confirmFit.push({ width, left: g.left, right: g.right })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }
  await testInfo.attach('batch-submit-confirm-containment.json', {
    body: JSON.stringify({ fit: confirmFit }, null, 2),
    contentType: 'application/json',
  })

  // Timing control, not a poll guard: shouldPollList (lib/invoices.ts:1259-1261) needs a
  // row in queued/submitted (both stay validated here) and useLiveRefresh never installs
  // a timer while inactive, so no interval exists to guard against either way. This only
  // rules out a confound for the uncheck-disarms assertion below -- that the bar might
  // disarm on its own over elapsed time, for a reason unrelated to the checkbox.
  await page.waitForTimeout(2500)
  await expect(confirmBtn, 'the armed bar must not self-disarm while every row stays validated').toBeVisible()

  // AC-1: change the selection -- disarm() fires from the checkbox's own onChange
  // (InvoicesList.tsx:579-584), same rule as ApprovalsView.tsx:142-152.
  await row2.getByTestId('invoice-select').uncheck()
  await expect(confirmBtn, 'a selection change must invalidate the arm').toHaveCount(0)
  await expect(cancelBtn).toHaveCount(0)
  await expect(page.getByTestId('batch-submit'), 'the bar returns to idle, not to armed').toBeVisible()
  expect(submissionPosts, 'the selection change must still have issued no POST').toHaveLength(0)

  // Re-arm with the one row left selected, then confirm -- submitSelected() is the file's
  // own arm+confirm+await-POST helper (Stage-1 correction C-2: still correct against
  // today's InvoicesList.tsx; APPR-16-04 touched only Pager props).
  await submitSelected(page)

  await expect(page.getByTestId('batch-submit-results')).toContainText(num1)
  await expect(page.getByTestId('batch-submit-results')).toContainText('Queued')
  expect(submissionPosts, 'exactly one POST for the whole journey -- the disarmed selection change sent nothing').toHaveLength(1)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('register-search: a term matching only a row past page 1 is found, and the count is the server total', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-01-05 search ${Date.now()}`, tin: freshTin() })
  // A TIN unique to the anchor alone -- the 50 bulk rows all keep cleanInvoiceFields' fixed
  // buyer_tin, so a match on this one can only be the anchor, which buildAnchoredPage always
  // places on page 2.
  const searchTin = freshTin()
  const { anchorNumber } = await buildAnchoredPage(token, entity.id, 50, { buyer_tin: searchTin })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  await expect(page.getByTestId('invoices-pager')).toContainText('OF 51')
  await expect(page.getByTestId('invoice-row').filter({ hasText: anchorNumber }), 'the anchor is the oldest row, so an unfiltered page 1 must not show it').toHaveCount(0)

  const searchResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('q') === searchTin,
  )
  await page.getByTestId('invoice-search-input').fill(searchTin)
  await page.getByTestId('invoice-search-input').press('Enter')
  const resp = await searchResp
  const body = (await resp.json()) as { pagination: { total: number } }

  // The unique TIN can only match the anchor -- proves the server actually narrowed the
  // set, not just that the pager echoes whatever total it was given (register-pagination
  // above already covers that half).
  expect(body.pagination.total, 'the unique buyer TIN must match exactly the anchor row').toBe(1)
  await expect(page.getByTestId('invoice-row').filter({ hasText: anchorNumber }), 'search must surface the row even though it starts past page 1').toBeVisible()
  await expect(page.getByTestId('invoices-pager')).toContainText(`OF ${body.pagination.total}`)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// task-334 (BUG-01-08): the Customers screen used to aggregate only the server's DEFAULT
// (unpaged) page -- limit=50 -- so a client past 50 buyers silently dropped the rest
// (the real production symptom this fixes: 47 of 256 buyers shown). Deliberately NOT
// cleanInvoiceFields' shared buyer_tin: each invoice here gets its OWN distinct buyer, so
// aggregateCustomers produces one row per invoice, never merging two into one.
function customersFields(invoiceNumber: string, buyerTin: string, buyerName: string) {
  return { ...cleanInvoiceFields(invoiceNumber), buyer_tin: buyerTin, buyer_name: buyerName }
}

test('customers-whole-set: every buyer appears and no KPI cards render', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-01-08 customers ${Date.now()}`, tin: freshTin() })

  // Mirrors buildAnchoredPage's own anchor idiom above: created FIRST, sequentially, so
  // `ORDER BY created_at DESC` sorts it OLDEST -- exactly the row a default (unpaged)
  // listInvoices call used to drop once a client passed 50 buyers.
  const BULK_COUNT = 50
  const stamp = Date.now()
  const anchorName = `BUG-01-08 Anchor Buyer ${stamp}`
  await createInvoice(token, { entity_id: entity.id, ...customersFields(`INV-BUG0108-ANCHOR-${stamp}`, freshTin(), anchorName) })
  await Promise.all(
    Array.from({ length: BULK_COUNT }, (_, i) =>
      createInvoice(token, { entity_id: entity.id, ...customersFields(`INV-BUG0108-BULK-${i}-${stamp}`, freshTin(), `BUG-01-08 Buyer ${i} ${stamp}`) }),
    ),
  )

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await page.getByRole('button', { name: /Customers/ }).click()
  await expect(page.getByRole('heading', { name: 'Customers & vendors' })).toBeVisible()

  await expect(
    page.getByText(anchorName),
    'a 50-row default-limit fetch would have dropped the oldest buyer -- proves the whole set was fetched',
  ).toBeVisible()
  await expect(page.locator('.pf-list-row')).toHaveCount(BULK_COUNT + 1)
  await expect(page.locator('.pf-grid-4'), 'no KPI card row').toHaveCount(0)
  await expect(page.getByText('Valid TINs')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// RED (task-335, BUG-01-09) -- ReportsView shares BUG-01-F's un-paged aggregation defect:
// it feeds `rows` from a single, un-paged listInvoices call (server default limit 50), so
// "Invoices in period"/"Top customers by value" undercount past 50. No nav helper and no
// data-testid exist on this surface yet (goToReports/reportsKpiValue below are this file's
// own, not shared with goToInvoices/openInvoiceRow -- Reports has none of the row/id hooks
// those assume).
async function goToReports(page: Page): Promise<void> {
  await page.getByRole('button', { name: /Reports/ }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Reports & analytics', exact: true })).toBeVisible()
}

// The KPI tile row's own markup (ReportsView.tsx): a `<div className="label">` holding the
// tile's label, sibling to the `.money` value span one level up -- AC #5 keeps this layout
// unchanged, only the underlying data source moves.
function reportsKpiValue(page: Page, label: string) {
  return page.locator(`xpath=//div[@class="label" and normalize-space(text())="${label}"]/parent::div/following-sibling::span[contains(@class,"money")]`)
}

test('reports-whole-set: the period invoice count covers the whole set', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-01-09 reports count ${Date.now()}`, tin: freshTin() })
  const BULK_COUNT = 50
  await buildAnchoredPage(token, entity.id, BULK_COUNT) // 1 anchor (oldest) + 50 bulk = 51

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToReports(page)

  // RED today: ReportsView's un-paged listInvoices call caps at the server's default page
  // (50), so a 51-invoice entity under-counts by exactly 1 here.
  await expect(reportsKpiValue(page, 'Invoices in period'), 'must equal the whole set, not the server default page').toHaveText(String(BULK_COUNT + 1))

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('reports-whole-set: top customers ranks over the whole set', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-01-09 reports top-cust ${Date.now()}`, tin: freshTin() })
  const stamp = Date.now()
  const anchorName = `BUG-01-09 Anchor Buyer ${stamp}`
  // Fixture design (the one open choice this subtask leaves to its test author): the 50
  // bulk rows all keep cleanInvoiceFields' shared buyer_tin, so aggregateCustomers collapses
  // them into ONE bucket totalling 50 x 1075 = 53,750. The anchor gets its OWN distinct
  // buyer_tin plus a total (60,000) that exceeds that whole summed bucket -- so it can only
  // head "Top customers by value" once ALL 51 rows, not just the newest 50, feed the
  // aggregation. The anchor is also the OLDEST row (buildAnchoredPage's own invariant), so
  // an un-paged, newest-50-only fetch never sees it at all: this fixture fails for the SAME
  // reason a page-1-only fetch would (missing row), not an incidental tie or rounding
  // artifact, which is what makes the assertion robust rather than just incidentally true.
  await buildAnchoredPage(token, entity.id, 50, { buyer_tin: freshTin(), buyer_name: anchorName, total: '60000.00' })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToReports(page)

  const card = page.locator('xpath=//span[contains(@class,"card-title") and normalize-space(text())="Top customers by value"]/ancestor::div[2]')
  const names = card.locator('span:not(.money):not(.card-title)')
  await expect(names).not.toHaveCount(0)
  await expect(names.first(), 'the anchor buyer must head the list once the whole set feeds the aggregation').toHaveText(anchorName)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// DOC-02-09: the source-document previewer, reached only from invoice detail (fence 4 --
// no other entry point). Folds the story's Test Specs rows 10+11 into ONE test() (Stage-1
// correction C1, the same fold this file already did once for M5-09-08 -- row 11 has no
// fixture of its own and this file's tests each get a fresh page/error collector).
// buildMixedCsv is deterministic (importFixtures.ts): header + STRUCT(2) + STRUCT(3) +
// VIOLATE(4) + CLEAN(5), rows_total 4 -- INV-UI-MIX-VIOLATE's source_rows is [4], so the
// card renders the SINGULAR "Row 4 of this file became this invoice." (lib/sourceDocument.ts).
test("invoice detail: the source-document card states the real range, and the modal opens on the whole file with this invoice's row marked", async ({
  page,
}) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `DOC-02 preview ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  // import-wizard.spec.ts's own proven E2E-04 recipe, reused verbatim (this file's own
  // Day-60 test above drives the identical steps).
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'doc02-mixed.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp

  await goToInvoices(page)
  await openInvoiceRow(page, 'INV-UI-MIX-VIOLATE')

  await expect(page.getByTestId('source-document-card')).toBeVisible()
  // Literal, not a `/Rows? \d+/` shape regex -- a shape regex passes for a WRONG row number
  // too, the DOC-01 empty-id failure class this assertion exists to prevent (Stage-1
  // correction C2).
  await expect(page.getByTestId('source-document-range')).toHaveText('Row 4 of this file became this invoice.')

  await page.getByTestId('view-source-document').click()
  const modal = page.getByTestId('source-document-modal')
  await expect(modal).toBeVisible()
  // Middle dot (U+00B7) escaped, not typed literally -- this file's own precedent
  // (:539-540) treats a non-ASCII assertion literal as a CI-shell encoding hazard.
  await expect(page.getByTestId('sheet-scope-file')).toHaveText(/^Whole file \u00b7 4 rows$/)
  await expect(page.getByTestId('sheet-scope-invoice')).toBeVisible()
  await expect(page.getByTestId('sheet-row-marked')).toHaveCount(1)
  await expect(page.getByTestId('sheet-status')).toHaveText(/SHOWING ALL 4 ROWS/)
  await expect(page.getByTestId('source-document-rail')).toBeVisible()
  // A SHA-256 hex digest chunked 16 chars/line is always exactly 4 lines (64 / 16).
  await expect(page.getByTestId('hash-line')).toHaveCount(4)

  await page.getByTestId('source-modal-close').click()
  await expect(modal).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The sheet's windowing is unit-tested as a pure function (lib/sourceDocument.test.ts
// pins sheetWindow at 1,479 and 100,000 rows), but the 4-row fixture above never
// scrolls, so no browser had ever rendered this component past one screen. The design
// brief's own warning is the failure this covers: without the clamp the measured
// viewport reads the full content height (~44,000px), every row renders, and the tab
// hangs. buildPerfCsv is import-wizard.spec.ts's proven 500-invoice/1500-row fixture.
test('invoice detail: a 1,500-row source file renders through the window, not all at once', async ({ page }) => {
  test.setTimeout(240_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `DOC-02 window ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"][accept=".csv,.xlsx"]')
    .setInputFiles({ name: 'doc02-window.csv', mimeType: 'text/csv', buffer: Buffer.from(buildPerfCsv(), 'utf8') })

  const previewResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 120_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await previewResp

  // buildPerfCsv shares buildMixedCsv's PERF_HEADER, so the same two hand-maps apply.
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const importResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 120_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp

  await goToInvoices(page)
  // Any of the 500 will do -- they all share the one file -- and openInvoiceRow does
  // not paginate, so naming a specific number would depend on the list's sort order.
  await page
    .getByTestId('invoices-list')
    .getByText(/^INV-UI-\d{5}$/)
    .first()
    .click()
  await expect(page.getByTestId('invoice-detail')).toBeVisible()

  await page.getByTestId('view-source-document').click()
  const modal = page.getByTestId('source-document-modal')
  await expect(modal).toBeVisible()

  // fmtPlain is toLocaleString('en-NG'), so four digits carry a thousands comma.
  await expect(page.getByTestId('sheet-scope-file')).toHaveText(/^Whole file · 1,500 rows$/)
  await expect(page.getByTestId('sheet-status')).toHaveText(/SHOWING ALL 1,500 ROWS/)

  // The window, not the file. An unclamped viewport measure renders all 1,500.
  const rendered = await page.getByTestId('sheet-row-number').count()
  expect(rendered, 'no rows rendered at all').toBeGreaterThan(0)
  expect(rendered, `${rendered} of 1,500 rows are in the DOM; the window is not bounding the render`).toBeLessThan(200)

  // Spacers are what keep the scrollbar honest while only a window exists.
  await expect(page.getByTestId('sheet-spacer-top')).toHaveCount(1)
  await expect(page.getByTestId('sheet-spacer-bottom')).toHaveCount(1)
  await expect(page.getByTestId('sheet-marker-track')).toBeVisible()
  await expect(page.getByTestId('marker-viewport')).toBeVisible()
  await expect(page.getByTestId('marker-invoice-block')).not.toHaveCount(0)

  // Scrolling must move the window. The modal opens auto-scrolled to THIS invoice's
  // rows, and which invoice the list surfaced first is not fixed, so both ends are
  // driven explicitly rather than measured from wherever entry happened to land.
  const firstRowNumber = async () => Number((await page.getByTestId('sheet-row-number').first().innerText()).trim())
  const scrollTo = async (top: number) =>
    page.getByTestId('sheet-scroll').evaluate((el, t) => {
      el.scrollTop = t
    }, top)

  await scrollTo(0)
  await expect
    .poll(firstRowNumber, { message: 'scrolling to the top did not show the first data row', timeout: 15_000 })
    .toBe(2)

  await scrollTo(30 * 1200)
  await expect
    .poll(firstRowNumber, { message: 'the window did not move after scrolling', timeout: 15_000 })
    .toBeGreaterThan(1000)

  const afterScroll = await page.getByTestId('sheet-row-number').count()
  expect(afterScroll, 'the window grew unbounded while scrolling').toBeLessThan(200)

  // No truncation notice: 1,500 is well inside the 5,000-row server cap, so a visible
  // one would mean the cap is being applied at the wrong threshold.
  await expect(page.getByTestId('sheet-truncation')).toHaveCount(0)

  await page.getByTestId('source-modal-close').click()
  await expect(modal).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('invoice detail: a manually created invoice shows the no-source state', async ({ page }) => {
  test.setTimeout(90_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `DOC-02 no-source ${Date.now()}`, tin: freshTin() })
  const invoiceNumber = `INV-DOC02-NOSRC-${freshTin()}`
  await createInvoice(token, { entity_id: entity.id, invoice_number: invoiceNumber })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  await expect(page.getByTestId('source-document-card')).toContainText('No source document')
  await expect(page.getByTestId('view-source-document')).toHaveCount(0)
  const whyButton = page.getByTestId('why-no-source-document')
  await expect(whyButton).toHaveText('Why there is no file')

  await whyButton.click()
  await expect(page.getByTestId('source-document-modal')).toBeVisible()
  await expect(page.getByTestId('source-document-no-source')).toContainText('There is no source document')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// BUG-05-05 (task-414): the parent story's AC-5 -- register and detail must agree on
// buyer_tin in every state, driving the SAME invoice through both surfaces per case
// rather than asserting each surface independently (they could coincidentally match on
// wrong values). 'malformed' is a present-but-invalid TIN and must render AS-IS, never
// the missing copy -- isBuyerTinMissing (lib/invoices.ts) is presence-only, not format.
test('buyer-tin: register and detail agree on missing, malformed, and well-formed TINs', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-05 buyer-tin ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  const cases: { label: string; buyerTin: string | undefined; expectMissing: boolean }[] = [
    { label: 'MISSING', buyerTin: undefined, expectMissing: true },
    { label: 'MALFORMED', buyerTin: 'BADTIN', expectMissing: false },
    { label: 'WELLFORMED', buyerTin: '87654321-0002', expectMissing: false },
  ]

  for (const c of cases) {
    const invoiceNumber = `INV-BUG05-${c.label}-${Date.now()}`
    const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber), buyer_tin: c.buyerTin })

    await goToInvoices(page)
    const row = invoiceRowByNumber(page, invoiceNumber)
    await expect(row).toBeVisible()
    const registerText = (await row.getByTestId('buyer-tin').textContent())?.trim()

    await openInvoiceRow(page, invoiceNumber)
    const detailText = (await page.getByTestId('buyer-tin').textContent())?.trim()

    // The real cross-surface comparison: both must render the identical text for the
    // SAME invoice, not two independent assertions that happen to coincide.
    expect(detailText, `${c.label}: register="${registerText}" detail="${detailText}"`).toBe(registerText)

    if (c.expectMissing) {
      expect(registerText, `${c.label} must render the shared missing-TIN copy`).toBe('TIN MISSING')
    } else {
      expect(registerText, `${c.label} must not render the missing-TIN copy`).not.toBe('TIN MISSING')
      expect(registerText, `${c.label} must render the exact stored value`).toBe(c.buyerTin)
    }

    // AC-5's own "and validates green": WELLFORMED is cleanInvoiceFields() untouched
    // (fires zero violations by construction, per that fixture's own doc comment) --
    // only this case's literal claim is checked, since the loop's other two are
    // deliberately never validated.
    if (c.label === 'WELLFORMED') {
      const validated = await validateInvoice(token, created.id)
      expect(validated.violations, `WELLFORMED should validate with zero violations, got ${JSON.stringify(validated.violations)}`).toEqual([])
      expect(validated.status, 'WELLFORMED should promote to validated').toBe('validated')
    }
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// BUG-04-07 (task-403): the UBL surface's two browser flows. Appended to this capability
// flow rather than split into a ubl.spec.ts -- invoice detail IS this file's capability
// (docs/e2e-convention.md). The endpoint's own headers, its 409/404 envelopes and its
// cross-tenant parity are invisible from a browser and live in e2e/api/contract-ubl.spec.ts,
// which is what keeps this layer thin.
//
// This import sits at the FOOT of the file, not the head: four files cite this spec by line
// (personaSession.ts:77, persona-surfaces.spec.ts:109, portfolio.spec.ts:71,
// rule_set_v3_test.go:386) and one inserted line at the top would shift every one of them.
// ESM hoists import declarations, so the position is style only.
import { readFileSync } from 'node:fs'

test("invoice detail: View UBL/XML renders the server's own document, and Download saves exactly those bytes", async ({
  page,
}) => {
  // Cold-fleet headroom, matching this file's own 90s precedent -- one sign-in, one detail
  // round trip, one UBL fetch and one download.
  test.setTimeout(90_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-04 ubl ${Date.now()}`, tin: freshTin() })
  // [A-Za-z0-9-] only, so XmlModal's ublFilename sanitiser is the identity here. Its
  // TRANSFORMATION is unit-covered (XmlModal.test.tsx); re-proving it in a browser would
  // duplicate the base of the pyramid.
  const invoiceNumber = `INV-BUG04-UBL-${Date.now()}`
  const inv = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  // AC1. cleanInvoiceFields carries an issue date, a currency, a buyer name and one line
  // item, and supplier_name is entity-derived ([supplier-from-entity]) -- so ubl.Missing is
  // empty, the control is live, and no reason renders beside it.
  const viewUbl = page.getByTestId('view-ubl')
  await expect(viewUbl).toBeVisible()
  await expect(viewUbl).toBeEnabled()
  await expect(viewUbl).toContainText('View UBL/XML')
  await expect(page.getByTestId('view-ubl-blocked-reason')).toHaveCount(0)

  // AC2. Armed BEFORE the click that causes it, and scoped to THIS invoice's id so nothing
  // else on the page can satisfy the predicate.
  const ublResponse = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith(`/invoices/${inv.id}/ubl`),
    { timeout: 30_000 },
  )
  await viewUbl.click()
  const served = await ublResponse
  expect(served.status(), 'the UBL route must answer 200 for a complete invoice').toBe(200)
  // Task AC #3. The exact header string is pinned in e2e/api/contract-ubl.spec.ts, where the
  // fetch seam is ours; here it only has to be XML.
  expect(served.headers()['content-type'], 'the document must arrive as XML').toMatch(/^application\/xml/)
  const servedXml = await served.text()
  // Without this floor the two equalities below would both pass on a pair of empty strings.
  expect(servedXml.length, 'the served document must be non-empty').toBeGreaterThan(0)
  expect(servedXml, 'the served body must be the UBL render, not an error envelope').toContain(
    `<cbc:ID>${invoiceNumber}</cbc:ID>`,
  )

  await expect(page.getByTestId('ubl-modal')).toBeVisible()
  const pre = page.getByTestId('ubl-xml')
  await expect(pre).toBeVisible()
  // textContent + toBe, never toHaveText: toHaveText NORMALIZES WHITESPACE, which would erase
  // the indentation this equality exists to pin. A client-assembled document fails here. Safe
  // as a one-shot read -- the <pre> is committed in one render, so once it is visible its
  // text is final.
  expect(await pre.textContent(), 'the <pre> must be the server body verbatim').toBe(servedXml)

  // AC6, ASCII substring only.
  await expect(page.getByTestId('ubl-provenance')).toContainText(
    'It is not a copy of what was transmitted to the access point.',
  )

  // AC3, and it must read the FILE. downloadUbl revokes the object URL synchronously one
  // statement after a.click() (XmlModal.tsx:34-35); a premature revoke surfaces as a
  // zero-byte or truncated saved file while the download event still fires, so an
  // event-only or presence-only assertion would pass on that bug. jsdom has no download
  // pipeline, so no unit row can observe it -- this is the only layer that can.
  const downloadEvent = page.waitForEvent('download', { timeout: 15_000 })
  await page.getByTestId('download-ubl').click()
  const download = await downloadEvent
  expect(await download.failure(), 'the download must complete').toBeNull()
  expect(download.suggestedFilename()).toBe(`${invoiceNumber}.xml`)
  const saved = readFileSync(await download.path(), 'utf8')
  expect(saved.length, 'a revoked object URL saves an empty file').toBeGreaterThan(0)
  expect(saved, 'the saved file must be the bytes the server served').toBe(servedXml)

  // AC4 -- a deployed-bundle spot check, not the oracle: the retired copy's absence is
  // proven by the BUG-04-06 source scan. Non-vacuous, the node exists and carries text.
  // ASCII substring only; the retired sentence's own quotes are U+201C/U+201D.
  await expect(page.getByTestId('ubl-modal')).not.toContainText('View XML')

  // Never click the `ubl-modal` locator to dismiss -- it resolves the SCRIM, which carries
  // onClick={onClose} (XmlModal.tsx:82), so it would pass for the wrong reason.
  await page.getByTestId('ubl-modal-close').click()
  await expect(page.getByTestId('ubl-modal')).toHaveCount(0)
  await expect(page.getByTestId('invoice-detail')).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test("invoice detail: an incomplete invoice shows a disabled View UBL/XML carrying the server's own reason", async ({
  page,
}) => {
  test.setTimeout(90_000)

  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `BUG-04 ubl gap ${Date.now()}`, tin: freshTin() })

  // ONE gap, deliberately. ubl.Missing (internal/ubl/ubl.go) reports in a fixed order and
  // "at least one line item" is LAST, so a single-gap invoice's sentence is nearly all of the
  // substring asserted below rather than a tail fragment -- and it is pure ASCII. The other
  // two gaps this seam might reach are unreachable in practice: Store.Create rejects a blank
  // invoice number pre-tx and overwrites supplier_name from the entity
  // ([supplier-from-entity]).
  const invoiceNumber = `INV-BUG04-GAP-${Date.now()}`
  await createInvoice(token, {
    entity_id: entity.id,
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    currency: 'NGN',
    buyer_name: 'Buyer Ltd',
  })

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  // AC5. Visible first: a click target that does not exist would make everything below
  // vacuous.
  const viewUbl = page.getByTestId('view-ubl')
  await expect(viewUbl).toBeVisible()
  await expect(viewUbl).toBeDisabled()
  // Substring only, never the em dash literal -- an encoding hazard through a CI shell
  // (:539-540).
  await expect(page.getByTestId('view-ubl-blocked-reason')).toContainText('at least one line item')

  // Two independent oracles: nothing left the browser, and nothing mounted.
  const noUbl = page.waitForRequest((r) => r.method() === 'GET' && new URL(r.url()).pathname.endsWith('/ubl'), {
    timeout: 2_000,
  })
  // force:true bypasses Playwright's actionability pre-checks, which would otherwise refuse a
  // disabled element and TIME OUT rather than assert -- but the real HTML `disabled`
  // attribute still suppresses the browser's click event, so React's onClick never runs.
  // Never dispatchEvent('click'): it bypasses that suppression and proves the opposite
  // guarantee (:556-561).
  await viewUbl.click({ force: true })
  await expect(noUbl).rejects.toThrow()
  await expect(page.getByTestId('ubl-modal')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// The firm tenant's policy is active tenant-wide (beforeAll's ensureFirmPolicyActive), so
// a freshly validated firm invoice always arms a run now -- the no-run case
// (APPR-13-06/task-548) this test originally covered is no longer reachable this way; its
// empty-trail branch moved to a draft-detail test instead (still reachable there, since a
// draft never arms anything). [topology-never-publishes] still holds: no
// armedInvoice()-style helper, no policy call anywhere below -- this test only validates,
// same as every other case in this file.
test('detail surface: the armed decision block and trail card, plus their layout at every wide width', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `APPR-13-06 armed ${Date.now()}`, tin: freshTin() })
  const invoiceNumber = `INV-APPR1306-${Date.now()}`
  const invoice = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })
  await validateInvoice(token, invoice.id)

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  // The real backend, over the real wire: this invoice's total sits below both firm
  // conditions, so materialise emits fin_mgr (ord0) then compliance (ord1), both pending.
  // PERSONAS.A holds neither seat, so the lowest-ord pending check (fin_mgr) fails and
  // both controls stay disabled with the AXIS-2 sentence (handlers.go:394) reaching the
  // browser as VISIBLE text, not only `title`. NOT a duplicate of InvoiceDetail.test.tsx:
  // 2798, which parametrizes all five gate sentences against a mock and never leaves it.
  const detailApprove = page.getByTestId('detail-approve')
  const detailReject = page.getByTestId('detail-reject')
  await expect(detailApprove).toBeVisible()
  await expect(detailApprove).toBeDisabled()
  await expect(detailReject).toBeVisible()
  await expect(detailReject).toBeDisabled()
  const approveReason = page.getByTestId('approve-blocked-reason')
  await expect(approveReason).toBeVisible()
  await expect(approveReason).toHaveText(
    "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role.",
  )
  // reject-blocked-reason never renders: handlers.go:506-509 assigns reject the same reason
  // as approve, and InvoiceDetail.tsx:769 only renders reject's own div when they differ.

  // The pending step and its role live on the TRAIL CARD, not the decision block above.
  // `Engagement Manager` is the FIRM tenant's fin_mgr title (db/seed.dev.sql:65) --
  // Honeywell's title for the same key is `Finance Manager` and would be wrong here.
  const trailState = page.getByTestId('approval-trail-state')
  await expect(trailState).toBeVisible()
  await expect(trailState).toHaveText('In progress')
  const trailSteps = page.getByTestId('approval-trail-step')
  await expect(trailSteps).toHaveCount(2)
  await expect(trailSteps.first()).toContainText('Approval · Engagement Manager')
  await expect(page.getByTestId('approval-trail-empty')).toHaveCount(0)

  // AC-1: containment -- decision block inside its action column, the file's own idiom
  // verbatim (:1626-1632). NOT assertFillsColumn here: the decision block right-aligns
  // and is legitimately narrower than its column, so a fill check would fail on correct
  // code. detail-decision-actions has no testid'd wrapper of its own (InvoiceDetail.tsx:
  // 633), so its parent is read via xpath, this file's own idiom for a testid-less
  // ancestor (roles.spec.ts:339).
  const decisionBlock = page.getByTestId('detail-decision-actions')
  const actionColumn = decisionBlock.locator('xpath=..')
  const viewUbl = page.getByTestId('view-ubl')
  const entryViewport = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const [blockBox, columnBox, ublBox] = await Promise.all([
        decisionBlock.boundingBox(),
        actionColumn.boundingBox(),
        viewUbl.boundingBox(),
      ])
      expect(blockBox && columnBox && ublBox, `decision block, its column and View UBL must all render at ${width}px`).toBeTruthy()
      const g = gaps(blockBox!, columnBox!)
      expect(g.left, `decision block must not start left of its column at ${width}px`).toBeGreaterThanOrEqual(0)
      expect(g.right, `decision block must not extend right of its column at ${width}px`).toBeGreaterThanOrEqual(0)

      // Both are direct children of the same alignItems:'flex-end' column, each emitted
      // as a fragment rather than a wrapper (InvoiceDetail.tsx:633/681-838) -- a wrapping
      // div in place of either fragment would break this.
      const blockRight = blockBox!.x + blockBox!.width
      const ublRight = ublBox!.x + ublBox!.width
      expect(Math.abs(blockRight - ublRight), `decision block's right edge must equal View UBL's at ${width}px`).toBeLessThanOrEqual(1)
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  // AC-4: the trail card matches its rail sibling's (status-history's) width -- both are
  // unstyled-width children of one flexDirection:'column' rail (InvoiceDetail.tsx:1083).
  // 1px, not assertFillsColumn's 24px default: that slack is sized for a scrollbar/list-
  // vs-bar gutter and would pass a card overflowing its own 16px padding.
  const trail = page.getByTestId('approval-trail')
  const statusHistory = page.getByTestId('status-history')
  const trailFit = await assertFillsColumn(page, trail, statusHistory, 'approval-trail vs status-history', 1)
  for (const entry of trailFit) {
    // assertFillsColumn's own bound only catches the trail card being too NARROW
    // (positive gaps); overflow yields NEGATIVE gaps that pass max(left,right)<=slack, so
    // this second bound is what catches the trail card being too WIDE.
    expect(entry.left, `trail card must not overflow status-history's left edge at ${entry.width}px`).toBeGreaterThanOrEqual(-1)
    expect(entry.right, `trail card must not overflow status-history's right edge at ${entry.width}px`).toBeGreaterThanOrEqual(-1)
  }

  // General console hygiene only now -- this invoice is armed, so its approval GET
  // returns 200, not a 404. D-27's no-console-error-on-404 observation moved with
  // approval-trail-empty to the "submit one invoice from its own page" test's draft
  // leg above, the only site left where a detail page's approval GET still 404s.
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// AUDIT-02-04. One page, two facts the unit tests cannot reach:
//
// 1. LEAK. APP_PERSONAS (frontend/app/src/auth.ts:34-61) holds BOTH tenants' admin
//    subjects, unscoped. The genesis row below is actored by Honeywell's admin inside the
//    FIRM tenant, so the RLS-scoped server finds no membership and answers kind 'raw'.
//    Any client-side fall-through to that table prints "Ngozi Balogun · Honeywell Group"
//    to an Okafor viewer.
// 2. CLIPPING. The rail is `1fr minmax(220px, 25%)` above 1180 (platform.css:233) and the
//    card sets overflow:hidden (InvoiceDetail.tsx:1315), so an unbreakable actor is
//    clipped SILENTLY -- no page-level overflow, no console line. A uuid breaks at its
//    hyphens; the ladder's email rung (internal/actor/actor.go:39-40) does not, and
//    db/seed.dev.sql:45 already holds a 44-char one. The second actor below is that shape.
//
// Both tokens are minted for subjects that hold no membership in the firm tenant --
// allowed on Preview posture, which is what the deploy gate runs (gateway.go:199).
// Needs `data-testid="status-history-actor"` on the actor span (InvoiceDetail.tsx:1335).
test('detail surface: a history actor the server cannot name renders verbatim and stays inside its rail', async ({ page }) => {
  test.setTimeout(120_000)
  const errors = collectErrors(page)

  const otherTenantAdmin = 'c0000000-0000-0000-0000-000000000002'
  const unbreakableActor = 'o.adebanjo-ogunleye@okaforandpartners.com.ng'

  const creatorToken = await login({ ...PERSONAS.A, subject: otherTenantAdmin })
  const validatorToken = await login({ ...PERSONAS.A, subject: unbreakableActor })
  const entity = await createEntity(creatorToken, { name: `AUDIT-02-04 actor ${Date.now()}`, tin: freshTin() })
  const invoiceNumber = `INV-AUDIT0204-${Date.now()}`
  const invoice = await createInvoice(creatorToken, { entity_id: entity.id, ...cleanInvoiceFields(invoiceNumber) })
  await validateInvoice(validatorToken, invoice.id)

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)
  await openInvoiceRow(page, invoiceNumber)

  // Positive control before either absence assertion: both rows render, in changed_at
  // order, each carrying its stored actor byte for byte and flagged mono.
  const actors = page.getByTestId('status-history-actor')
  await expect(actors).toHaveCount(2)
  await expect(actors.nth(0)).toHaveText(otherTenantAdmin)
  await expect(actors.nth(1)).toHaveText(unbreakableActor)
  await expect(actors.nth(0)).toHaveClass(/mono/)
  await expect(actors.nth(1)).toHaveClass(/mono/)

  const card = page.getByTestId('status-history')
  await expect(card).not.toContainText('Ngozi Balogun')
  await expect(card).not.toContainText('Honeywell Group')

  // Containment, never a width: a dimension bound passes on the very clipping it should
  // catch (BUG-03-05, e2e/topology/layout.ts:4-8). getBoundingClientRect reports LAYOUT
  // geometry, so the card's overflow:hidden hides the defect from the eye but not from
  // gaps(). 1px, matching the trail-card check at :2416-2423, not assertFillsColumn's
  // 24px: this cell is legitimately narrower than its card, so only overflow is a defect.
  const entryViewport = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      const cardBox = await card.boundingBox()
      expect(cardBox, `the status history card must render at ${width}px`).toBeTruthy()

      const boxes = await Promise.all((await actors.all()).map((a) => a.boundingBox()))
      expect(boxes, `both actor cells must render at ${width}px`).toHaveLength(2)
      for (const [i, box] of boxes.entries()) {
        expect(box, `actor cell ${i} must render at ${width}px`).toBeTruthy()
        const g = gaps(box!, cardBox!)
        expect(g.left, `actor cell ${i} must not start left of the status history card at ${width}px`).toBeGreaterThanOrEqual(-1)
        expect(g.right, `actor cell ${i} must not extend right of the status history card at ${width}px`).toBeGreaterThanOrEqual(-1)
      }
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
