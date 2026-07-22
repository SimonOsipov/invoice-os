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
  const violateId = violateEntry!.invoice_id!

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

  // 4. Open the violating invoice's live detail. The Invoices list is TENANT-GLOBAL with
  // no entity filter ([D8], internal/invoice/handlers.go), and "INV-UI-MIX-VIOLATE" is a
  // FIXED invoice_number buildMixedCsv() recreates on every run -- including
  // import-wizard.spec.ts's own E2E-04/05/09, against this SAME shared never-reset dev
  // DB -- so by the time this arc reaches Invoices, MULTIPLE rows can carry that exact
  // text. A plain `getByText('INV-UI-MIX-VIOLATE', {exact:true}).click()` hits
  // Playwright's strict-mode "more than one element" error the moment a second row with
  // that text exists (guaranteed once this suite has run more than once against the
  // shared DB). InvoicesList.tsx's List query orders `created_at DESC, id DESC`
  // (internal/invoice/store.go) and the component maps the fetched array in DOM order
  // with no client-side re-sort/filter/dedupe -- so row N in the DOM is exactly element N
  // of the list response's JSON array. Disambiguate by capturing THAT response, finding
  // the CAPTURED violateId's array index, and clicking the row at that exact index --
  // deterministic regardless of how many older same-numbered rows exist, rather than by
  // non-unique visible text. (Not a re-derivation of import-wizard.spec.ts's E2E-05,
  // which additionally proves the click-through-honest-placeholder invariant -- out of
  // scope for this arc's own moment of value, the business flow + dashboard rollup.)
  const listResp = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices'),
  )
  await page.getByRole('button', { name: /Invoices/ }).click()
  const listBody = (await (await listResp).json()) as { invoices: { id: string }[] }
  const rowIndex = listBody.invoices.findIndex((inv) => inv.id === violateId)
  expect(
    rowIndex,
    'the freshly-imported INV-UI-MIX-VIOLATE invoice must appear in the default (most-recent-first, limit-50) invoices page',
  ).toBeGreaterThanOrEqual(0)
  await expect(page.getByTestId('invoices-list')).toBeVisible()

  const detailResp = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith(`/api/invoice/v1/invoices/${violateId}`),
  )
  await page.getByTestId('invoice-row').nth(rowIndex).click()
  await detailResp
  await expect(page.getByTestId('invoice-detail')).toBeVisible()
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

  // 7a. Dashboard rollup ready state (Gap 1) -- existence/ready only, never a tenant-wide
  // count ([dashboard-ready-not-counted]): the shared dev DB accumulates invoices across
  // every run, so only the overview label + a rendered "<N> TOTAL" donut total are
  // asserted, never a specific N.
  await page.getByRole('button', { name: /Overview/ }).click()
  await expect(page.getByText('/ COMPLIANCE OVERVIEW', { exact: true })).toBeVisible()
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
