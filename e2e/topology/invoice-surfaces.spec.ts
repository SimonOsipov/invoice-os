// M4-09-06 (task-187): the focused topology e2e for the live invoice list +
// detail surfaces (M4-09-04/M4-09-05) -- mirrors M3-09's validation-playground
// pattern (topology.spec.ts: firm-persona sign-in -> wait for the /v1/me
// verified marker -> drive the live surface). NOT the M4-14 demo script
// ([focused-e2e-topology], out of scope per the M4-09 story).
//
// db/seed.dev.sql now seeds 27 invoices, but only across 6 of its 27 curated
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
import { test, expect, type Page } from '@playwright/test'
import { login, createEntity, createInvoice, validateInvoice, transitionInvoice, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { buildMixedCsv } from '../importFixtures'
import { APP_URL, FIRM_PERSONA, VALIDATION_EXPECTED } from './targets'

// collectErrors()/signInFirm(): the same console/pageerror + firm-persona
// sign-in idiom topology.spec.ts and import-wizard.spec.ts each inline (no
// spec file in this package exports its own helpers today, so this is a third
// copy, not a new seam).
function collectErrors(page: Page): string[] {
  const errors: string[] = []
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text())
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
// lowercase "New invoice" CTA can never collide. A row click routes through
// InvoicesList's onClick -> ctx.openImportedInvoice(id) -> the SAME live-detail
// seam an imported invoice uses ([reuse-imported-seam], InvoicesList.tsx) --
// clicking ANY real invoice's row opens LiveInvoiceDetail, not the mock
// placeholder, so this needs no import-flow detour at all.
async function goToInvoices(page: Page): Promise<void> {
  await page.getByRole('button', { name: /Invoices/ }).click()
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
// among 25+ others on the shared, never-reset dev DB. Sidebar.tsx:
// data-testid="company-switcher" (the toggle button) / "company-switcher-option" (each
// row in the open dropdown).
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// A supplier/buyer/line-item shape that fires EXACTLY
// ['supplier-tin-format', 'vat-standard-rate'] against the active v2 rule set
// (fixtures.ts's BAD_INVOICE_KEYS, re-verified here against the flat
// createInvoice wire shape): a malformed supplier TIN plus a VAT that isn't
// 7.5% of the subtotal. Every OTHER v1/v2 rule (the required/format/range
// rules, line-items-required, line-items-sum-subtotal, line-cost-non-negative)
// is satisfied so no incidental third violation sneaks in and breaks an
// exact-key assertion.
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

function submittableInvoiceFields(invoiceNumber: string, buyerTin: string) {
  return { ...cleanInvoiceFields(invoiceNumber), buyer_tin: buyerTin }
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

// submitSelected(): clicks batch-submit and waits for the POST .../invoices/submissions
// response. Unlike a list GET, this URL is unambiguous -- a poll tick never POSTs
// ([waitForResponse-on-the-list-is-poll-ambiguous] only applies to the list's GET) -- so
// this needs none of that care. Shared by every submit click below: the happy-path
// test's only submit, and the reject test's initial submit and its resubmit leg.
async function submitSelected(page: Page): Promise<void> {
  const resp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices/submissions'),
  )
  await page.getByTestId('batch-submit').click()
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

  const qr = page.getByTestId('fiscal-qr')
  await expect(qr).toBeVisible()
  await expect(qr).toHaveAttribute('src', /^data:image\/png;base64,/)
  await expect.poll(() => qr.evaluate((el) => (el as HTMLImageElement).naturalWidth)).toBeGreaterThan(0)
}

test('list surface: real rows render with real status badges, and Needs attention re-fetches server-side', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-09 list ${Date.now()}`, tin: freshTin() })

  // attn: created with the bad fixture, then validated via the API (not the
  // UI -- this test is the LIST surface, not the detail fix loop the second
  // scenario below drives). A blocking violation on a draft invoice is exactly
  // the needs_attention=true predicate (internal/invoice/needs_attention_test.go's
  // matchesNeedsAttentionPredicate: rejected/failed always match; a draft
  // matches iff it carries a severity:"error" violation).
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

test('detail surface: violations render against the rule-set version, the fix loop clears them, and status history records both the round trip and the not-a-draft regression', async ({
  page,
}) => {
  // Multiple live round trips through the detail surface (not-validated -> 3x
  // Re-validate + 1 edit + a remount) on a possibly cold fleet -- default 60s
  // is tight for that many sequential awaits, mirroring import-wizard's own
  // headroom bump for its own multi-round-trip test.
  test.setTimeout(90_000)

  const errors = collectErrors(page)

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
  const violationsTable = page.getByTestId('violations-table')
  await page.getByTestId('revalidate').click()
  await expect(violationsTable).toBeVisible()
  await expect(page.getByTestId('not-validated')).toHaveCount(0)
  for (const key of ['supplier-tin-format', 'vat-standard-rate']) {
    await expect(violationsTable).toContainText(key)
  }
  await expect(violationsTable.locator('tbody tr').first().locator('td').last()).toHaveText(String(VALIDATION_EXPECTED.ruleSetVersion))
  await expect(page.getByTestId('invoice-status-badge')).toContainText('DRAFT')
  await expect(page.getByTestId('status-history-row')).toHaveCount(1)

  // 3. The fix: edit the two broken fields (supplier TIN, VAT) AND -- the
  //    priority regression QA flagged -- edit issue_date with a plain
  //    YYYY-MM-DD value, the form's own placeholder shape. Before the QA fix
  //    (commit 0bfc4a1), sending a bare date 400'd at the backend
  //    (editReq.IssueDate decodes into a *time.Time, which only accepts a
  //    full RFC3339 string); diffEditInput now normalizes a bare date to
  //    midnight UTC first. onSaved firing (staleSinceEdit becoming true,
  //    asserted below) is the one behaviour a 400 could never produce -- a
  //    failed submit takes the catch branch and renders a red inline error
  //    instead, never calling onSaved.
  //
  //    The 3 inputs are matched by their own label text via XPath sibling
  //    lookup: the form carries no per-field test ids, and the two TIN inputs
  //    share the same placeholder ("########-####"), so a placeholder-based
  //    locator would be ambiguous.
  const form = page.getByTestId('edit-invoice')
  await form.locator('xpath=.//div[normalize-space(text())="Issue date"]/following-sibling::input').fill('2026-02-01')
  await form.locator('xpath=.//div[normalize-space(text())="Supplier TIN"]/following-sibling::input').fill(freshTin())
  await form.locator('xpath=.//div[normalize-space(text())="VAT"]/following-sibling::input').fill('75')
  await page.getByRole('button', { name: 'Save changes' }).click()

  await expect(page.getByTestId('stale-verdict')).toBeVisible()
  await expect(form).not.toContainText('Something went wrong')

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

  // 6. The other priority regression QA flagged: Re-validate on an untouched
  //    VALIDATED invoice must surface an inline error, not fail silently.
  //    isFixable(status) keeps the button visible for validated too
  //    ([gate-scope-draft-only] doc comment, InvoiceDetail.tsx), but
  //    Store.ApplyValidation's gate is draft-only -> 409 ErrNotDraft ->
  //    "invoice is not a draft" (handlers.go's statusForErr), forwarded
  //    verbatim as ApiError.message and rendered inline -- and nothing else
  //    changes.
  await page.getByTestId('revalidate').click()
  await expect(page.getByText('invoice is not a draft')).toBeVisible()
  await expect(page.getByTestId('status-history-row')).toHaveCount(2)
  await expect(page.getByTestId('invoice-status-badge')).toContainText('VALIDATED')

  // Chromium unconditionally logs "Failed to load resource … 409" to the
  // console for step 6's deliberate not-a-draft fetch, regardless of how
  // gracefully the app handles the response -- unsuppressable from app JS.
  // The 409 itself is already positively verified above (the inline "invoice
  // is not a draft" error rendering, history staying at 2 rows, status
  // staying VALIDATED), so this filters out ONLY that one expected resource-
  // load message; any other console error still fails the gate below.
  const unexpectedErrors = errors.filter((e) => !/Failed to load resource.*\b409\b/.test(e))
  expect(unexpectedErrors, `console errors on the app:\n${unexpectedErrors.join('\n')}`).toEqual([])
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
  const select = page.locator('select')
  await expect(select, 'entity picker <select> not found -- check VITE_GATEWAY_URL is configured for this deployed build').toBeVisible({
    timeout: 30_000,
  })
  await select.selectOption({ label: entity.name })

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
  const form = page.getByTestId('edit-invoice')
  await form.locator('xpath=.//div[normalize-space(text())="VAT"]/following-sibling::input').fill('75')
  await page.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByTestId('stale-verdict')).toBeVisible()
  await expect(form).not.toContainText('Something went wrong')

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
  // page now shows the ACTIVE entity's OWN scoped total, not the shared dev DB's
  // ever-growing tenant-wide count ([dashboard-ready-not-counted] is retired by that same
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
  await expect(clientRow, 'fresh entity health pill must flip to ALL CLEAR once its only violation is fixed').toContainText('ALL CLEAR')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// M5-09-08 (task-256): the M5 milestone gate itself -- both demonstrations Core AC #5
// requires, entirely from the browser, extending this capability flow rather than adding
// a dated spec ([capability-not-date], docs/e2e-convention.md; Stage-1 correction C12).
// The detail surface carries NO submit control in any status ([detail-submit-single]
// dropped) -- every submit below goes through the list's batch-select-and-submit path
// (invoice-select + batch-submit, M5-09-06), reused for BOTH the first submit and the
// resubmit leg.
//
// Row 1-3 of the story's Test Specs table ("batch-select and submit", "badge advances",
// "shows a real IRN and rendered QR") are folded into ONE test below (Stage-1 correction
// C13): row 2 says "continuing from the step above" and has no fixture of its own, and
// this file uses no test.describe.serial / module state that would let three independent
// test()s share a fixture -- each test() gets a fresh page and a fresh error collector.

test('submission surface: batch-select and submit a validated invoice, badge advances to ACCEPTED, and its detail shows a real IRN and a rendered QR', async ({
  page,
}) => {
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

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const row = invoiceRowByNumber(page, invoiceNumber)
  await row.getByTestId('invoice-select').check()
  await submitSelected(page)

  // AC-3: exactly one POST is what waitForResponse above already pinned (a single click);
  // the results panel names THIS invoice as queued.
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

  // Resubmit leg: back to the list -- the detail surface carries no submit control in any
  // status, so the ONLY way to resubmit is the same batch-select-and-submit path the
  // first submit used (AC-3).
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
  await transitionInvoice(token, inv.id, 'queued')
  await transitionInvoice(token, inv.id, 'failed')

  await signInFirm(page)
  await selectEntity(page, entity.name)
  await goToInvoices(page)

  const row = invoiceRowByNumber(page, invoiceNumber)

  // AC-8: a failed row can never be batch-selected -- isRowSelectable is `validated`-only.
  await expect(row.getByTestId('invoice-select')).toBeDisabled()

  await openInvoiceRow(page, invoiceNumber)

  await expect(page.getByTestId('failed-dead-end')).toBeVisible()
  await expect(page.getByTestId('failed-dead-end')).toContainText('cannot be re-driven')
  // "no submit control at all": failed is not `isFixable` (draft/validated/rejected
  // only), so neither Re-validate nor the edit form renders -- and no button anywhere on
  // this page matches /submit/i (InvoicesList, the only surface with a Submit button, is
  // fully unmounted while on the detail view -- App.tsx's view switch is exclusive, never
  // both mounted at once).
  await expect(page.getByTestId('revalidate')).toHaveCount(0)
  await expect(page.getByTestId('edit-invoice')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /submit/i })).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
