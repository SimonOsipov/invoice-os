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
  // [failed-no-reason-lands-on-the-detail] (task-332, BUG-01-06): this invoice's
  // rejection_reasons is [] (API-transitioned straight to failed, never rejected by the
  // APP) -- the card must say so honestly, not render a silent gap.
  await expect(page.getByTestId('failed-dead-end')).toContainText(/no reason recorded/i)
  // "no submit control at all": `can_edit` is false for a failed invoice
  // ([gates-on-the-wire], store.go's canEdit/canTransition), so the actions bar (Edit AND
  // Re-validate together) renders nothing at all -- `edit-invoice` itself would be vacuous
  // here (the form only mounts once Edit is clicked, and there is no Edit to click), so
  // `invoice-actions`/`edit-toggle` are the real guard ([actions-visibility]). No button
  // anywhere on this page matches /submit/i either (InvoicesList, the only surface with a
  // Submit button, is fully unmounted while on the detail view -- App.tsx's view switch is
  // exclusive, never both mounted at once).
  await expect(page.getByTestId('revalidate')).toHaveCount(0)
  await expect(page.getByTestId('invoice-actions')).toHaveCount(0)
  await expect(page.getByTestId('edit-toggle')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /submit/i })).toHaveCount(0)

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
