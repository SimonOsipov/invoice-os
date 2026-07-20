// M4-09-06 (task-187): the focused topology e2e for the live invoice list +
// detail surfaces (M4-09-04/M4-09-05) -- mirrors M3-09's validation-playground
// pattern (topology.spec.ts: firm-persona sign-in -> wait for the /v1/me
// verified marker -> drive the live surface). NOT the M4-14 demo script
// ([focused-e2e-topology], out of scope per the M4-09 story).
//
// db/seed.dev.sql seeds zero invoices (only business_entities, M4-22-03), so
// every scenario below creates its OWN entity + invoice(s) via e2e/api/client.ts
// BEFORE driving the UI -- the same "own entity per test" discipline as
// import-wizard.spec.ts (no-duplicate-invoice-number is scoped per entity, and
// this suite runs fullyParallel with retries:1 in CI, against the same shared
// firm-persona tenant every other topology spec also drives).
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
import { login, createEntity, createInvoice, validateInvoice, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
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
  const res = await page.goto(APP_URL)
  expect(res, `no response from ${APP_URL}`).toBeTruthy()
  expect(res!.ok(), `${APP_URL} returned HTTP ${res!.status()}`).toBeTruthy()
  await page.getByRole('button', { name: new RegExp(FIRM_PERSONA.buttonName) }).click()
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

async function openInvoiceRow(page: Page, invoiceNumber: string): Promise<void> {
  await page.getByText(invoiceNumber, { exact: true }).click()
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
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
