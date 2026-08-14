// M4-14-03 (task-210): the portfolio capability spec -- edit-client (Gap 2), create-client
// (day30 AC-3 preserved), status-pill (day30 AC-2 preserved), and client health pill
// (Gap 3, steady-state). ClientsView/EntityFormModal carry no data-testid
// ([no-testids-on-portfolio-dashboard], grep-verified) -- every selector below is by
// role/exact-text/CSS-class, the same idiom day30.spec.ts/topology.spec.ts already used
// for this surface before this story split them out.
//
// Archive/restore via the per-row action is now in scope (BUG-01-12): ClientsView's
// fifth column exercises offboardEntity/onboardEntity through the UI (arm-then-confirm).
// offboardEntity() (../api/client) is ALSO still used, as before, as a pure API-seam seed
// by the status-pill/filter specs below, and additionally as an out-of-band race tool by
// the 409 spec.
import { test, expect, type Locator, type Page } from '@playwright/test'
import { login, createEntity, createInvoice, validateInvoice, offboardEntity, PERSONAS } from '../api/client'
import { freshTin } from '../api/fixtures'
import { APP_URL, FIRM_PERSONA } from './targets'

// collectErrors()/signInFirm(): the same console/pageerror + firm-persona sign-in idiom
// topology.spec.ts, import-wizard.spec.ts, and invoice-surfaces.spec.ts each already
// inline or define locally (no spec file in this package exports its own helpers today) --
// this is a fourth copy, not a new seam.
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

// goToClients(): the Clients sidebar nav button (glyphs.tsx's NAV_CLIENTS, label
// "Clients") -- no data-testid on this surface, so a plain role/name click, mirroring
// invoice-surfaces.spec.ts's own inline `page.getByRole('button', { name: /Clients/ })`
// (Day-60 arc). Extracted here since every test in this file drives Clients.
async function goToClients(page: Page): Promise<void> {
  await page.getByRole('button', { name: /Clients/ }).click()
}

// selectEntity(): a local copy of persona-surfaces.spec.ts's own helper of the same name
// (that file exports nothing -- spec files don't import each other's module graph, see
// this file's own precedent above for badInvoiceFields). Opens the firm workspace switcher
// and picks the named company, making it the currently-open workspace.
async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// archiveAction(): the per-row archive/restore button (BUG-01-12) -- the row itself has an
// onClick (edit modal) but no button of its own, so the row's one <button> is unambiguous.
// Copy is unpinned (executor's call), so this locates structurally, never by label text.
function archiveAction(row: Locator): Locator {
  return row.locator('button').first()
}

// badInvoiceFields(): mirrors invoice-surfaces.spec.ts's own helper of the same name --
// fires exactly ['supplier-tin-format', 'vat-standard-rate'] against the active v1/v2 rule
// set (a malformed supplier TIN plus a VAT that isn't 7.5% of the subtotal; every other
// rule is satisfied). Duplicated locally rather than imported -- spec files don't import
// each other's module graph (invoice-surfaces.spec.ts:290-295's own rationale for the same
// discipline). Only used here to seed exactly ONE blocking violation on ONE invoice, so the
// fresh entity's needs_attention count lands at exactly 1.
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

test('edit-client: row click opens the edit modal, and Save issues PATCH /entities/{id} and the row reflects the new sector', async ({
  page,
}) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-14 portfolio edit ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await goToClients(page)

  // Reused across the pre- and post-Save assertion below -- Playwright locators re-resolve
  // against the live DOM on every `expect`, not a stale snapshot (same idiom as
  // invoice-surfaces.spec.ts's `clientRow`).
  const row = page.locator('.pf-list-row').filter({ hasText: entity.name })
  await expect(row).toBeVisible()
  await row.click()

  // EntityFormModal edit mode: role="dialog", visible title "Edit client"
  // (EntityFormModal.tsx:57,133).
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('Edit client')

  // The Sector field has no label association (a plain sibling <div>, EntityFormModal.tsx:
  // 178-179) -- located by xpath sibling lookup, the same idiom invoice-surfaces.spec.ts
  // uses for the edit-invoice form's Issue date/Supplier TIN/VAT fields.
  const newSector = `Fintech ${Date.now()}`
  await dialog
    .locator('xpath=.//div[normalize-space(text())="Sector (optional)"]/following-sibling::input')
    .fill(newSector)

  const patchResp = page.waitForResponse(
    (r) => r.request().method() === 'PATCH' && new URL(r.url()).pathname.endsWith(`/api/portfolio/v1/entities/${entity.id}`),
  )
  await page.getByRole('button', { name: 'Save changes' }).click()
  await patchResp

  // onSuccess -> list.run() refetches + closes the modal (EntityFormModal.tsx:99-100,
  // ClientsView.tsx:189-192) -- the row's Sector cell (ClientsView.tsx:168) shows the new
  // value once the refetch settles.
  await expect(row).toContainText(newSector)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// day30 AC-3 preserved: the ONLY browser-driven create-mode coverage of EntityFormModal --
// every other spec in this suite creates entities via the createEntity API seam. Relocated
// from day30.spec.ts:166-187 (the "Add-client modal" round trip), shape preserved verbatim.
test('create-client: the Add-client control opens create mode, and submit on a fresh TIN renders a new row (day30 AC-3 preserved)', async ({
  page,
}) => {
  const errors = collectErrors(page)

  await signInFirm(page)
  await goToClients(page)

  const onboardTin = freshTin()
  const onboardName = `M4-14 portfolio create ${onboardTin}`

  // "Add client" trigger button (ClientsView.tsx:119-125) -> EntityFormModal create mode
  // (role="dialog", EntityFormModal.tsx:127-131).
  await page.getByRole('button', { name: 'Add client' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  // The Name field has no label association (a plain sibling <div>, EntityFormModal.tsx:
  // 154-155), so target the first `.pf-input`; the TIN input is the only one carrying the
  // ########-#### placeholder (EntityFormModal.tsx:166).
  await dialog.locator('.pf-input').first().fill(onboardName)
  await dialog.getByPlaceholder('########-####').fill(onboardTin)
  // Submit button reads "Add client" in create mode (EntityFormModal.tsx:192-193); scoped
  // to the dialog so it doesn't collide with the trigger button of the same label.
  await dialog.getByRole('button', { name: 'Add client' }).click()

  // onSuccess refetches the list + closes the modal (ClientsView.tsx:127-130 equiv). The
  // new row's name span (ClientsView.tsx:164) is display-only text, so getByText matches
  // the list row, not the -- now closed -- form.
  await expect(page.getByText(onboardName, { exact: true })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// day30 AC-2 preserved, RE-SCOPED PER-ROW: a fresh ACTIVE entity plus a fresh entity
// archived through offboardEntity (API-only seed -- see the file-level comment above;
// never a UI action) -- the entity-STATUS pill (ClientsView.tsx:169-174 / portfolio.ts:
// 94-97), distinct from the health pill this file's next test drives. Deliberately NOT
// day30's `.getByText('ACTIVE').first()` against its own >=25-row preseeded portfolio --
// scoped instead to each fresh entity's own row (see the in-body comment below for why).
test('status-pill: a fresh active entity and a fresh offboardEntity-archived entity render ACTIVE and ARCHIVED pills (day30 AC-2 preserved)', async ({
  page,
}) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const activeEntity = await createEntity(token, { name: `M4-14 portfolio active ${Date.now()}`, tin: freshTin() })
  const archivedEntity = await createEntity(token, { name: `M4-14 portfolio archived ${Date.now()}`, tin: freshTin() })
  await offboardEntity(token, archivedEntity.id)

  await signInFirm(page)
  await goToClients(page)

  const activeRow = page.locator('.pf-list-row').filter({ hasText: activeEntity.name })
  const archivedRow = page.locator('.pf-list-row').filter({ hasText: archivedEntity.name })
  await expect(activeRow).toBeVisible()
  await expect(archivedRow).toBeVisible()

  // Status pills render the uppercase label ACTIVE/ARCHIVED (portfolio.ts:95-96) -- scoped
  // to each fresh entity's own row rather than `.first()` (day30's approach against a
  // >=25-row preseeded portfolio) so this assertion is deterministic on the shared,
  // non-reset dev DB regardless of what else already exists there.
  await expect(activeRow.getByText('ACTIVE', { exact: true })).toBeVisible()
  await expect(archivedRow.getByText('ARCHIVED', { exact: true })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Gap 3 (steady-state; the live-update half is covered by invoice-surfaces.spec.ts's Day-60
// arc). ClientsView.tsx's HealthCell/entityHealth (lib/dashboard.ts:153-158): an entity with
// zero invoices ever created has no row in the rollup's `clients` (INNER JOIN) -> "NO
// INVOICES YET"; an entity with exactly one needs_attention invoice -> "1 NEEDS ATTENTION".
// The third health state (ALL CLEAR -- a validated entity with zero violations) is NOT
// covered here: the story's AC #4 names only these two cases, and invoice-surfaces.spec.ts's
// Day-60 arc already exercises ALL CLEAR as its own post-fix assertion. Adding it here would
// be scope creep on a test-and-docs-only subtask.
test('health-pill: a fresh entity with a needs-attention invoice reads "1 NEEDS ATTENTION"; a fresh entity with no invoices reads "NO INVOICES YET"', async ({
  page,
}) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)

  const attnEntity = await createEntity(token, { name: `M4-14 portfolio attn ${Date.now()}`, tin: freshTin() })
  const attnInvoice = await createInvoice(token, {
    entity_id: attnEntity.id,
    ...badInvoiceFields(`INV-M414-PF-ATTN-${Date.now()}`),
  })
  await validateInvoice(token, attnInvoice.id)

  const emptyEntity = await createEntity(token, { name: `M4-14 portfolio empty ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await goToClients(page)

  const attnRow = page.locator('.pf-list-row').filter({ hasText: attnEntity.name })
  const emptyRow = page.locator('.pf-list-row').filter({ hasText: emptyEntity.name })
  // Exact because this fixture creates no approval_runs, and needs_attention's approval arm is
  // draft-with-a-latest-rejected-run only (TestStoreRollup_ApprovalRejectedArmIsDraftOnly,
  // TestStoreRollup_NeedsAttentionIncludesApprovalRejected for the run-less control).
  await expect(attnRow).toContainText('1 NEEDS ATTENTION')
  await expect(emptyRow).toContainText('NO INVOICES YET')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Same fresh active + fresh offboardEntity-archived seed as the status-pill test above
// (no new API helper needed).
test('status-filter: each position requests its status and renders only those rows', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const activeEntity = await createEntity(token, { name: `M4-14 portfolio filter-active ${Date.now()}`, tin: freshTin() })
  const archivedEntity = await createEntity(token, { name: `M4-14 portfolio filter-archived ${Date.now()}`, tin: freshTin() })
  await offboardEntity(token, archivedEntity.id)

  await signInFirm(page)
  await goToClients(page)

  const activeRow = page.locator('.pf-list-row').filter({ hasText: activeEntity.name })
  const archivedRow = page.locator('.pf-list-row').filter({ hasText: archivedEntity.name })
  await expect(activeRow).toBeVisible()
  await expect(archivedRow).toBeVisible()

  function entitiesRequest(matchesStatus: (status: string | null) => boolean) {
    return page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        new URL(r.url()).pathname.endsWith('/api/portfolio/v1/entities') &&
        matchesStatus(new URL(r.url()).searchParams.get('status')),
    )
  }

  const activeReq = entitiesRequest((s) => s === 'active')
  await page.getByRole('button', { name: 'Active' }).click()
  await activeReq
  await expect(activeRow).toBeVisible()
  await expect(archivedRow).not.toBeVisible()

  const archivedReq = entitiesRequest((s) => s === 'archived')
  await page.getByRole('button', { name: 'Archived' }).click()
  await archivedReq
  await expect(archivedRow).toBeVisible()
  await expect(activeRow).not.toBeVisible()

  const allReq = entitiesRequest((s) => s === null)
  await page.getByRole('button', { name: 'All' }).click()
  await allReq
  await expect(activeRow).toBeVisible()
  await expect(archivedRow).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Proves the header count equals the rows actually rendered under each position.
// Structural (rendered-row-count vs. parsed-header-number), not a hardcoded portfolio
// size: every spec in this run adds to the same portfolio (docs/e2e-convention.md).
test('status-filter: the header count follows the filter', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const activeEntity = await createEntity(token, { name: `M4-14 portfolio count-active ${Date.now()}`, tin: freshTin() })
  const archivedEntity = await createEntity(token, { name: `M4-14 portfolio count-archived ${Date.now()}`, tin: freshTin() })
  await offboardEntity(token, archivedEntity.id)

  await signInFirm(page)
  await goToClients(page)
  await expect(page.locator('.pf-list-row').filter({ hasText: activeEntity.name })).toBeVisible()

  async function assertCountMatchesRenderedRows(): Promise<void> {
    const rowCount = await page.locator('.pf-list-row').count()
    const headerText = (await page.locator('h1:has-text("Client portfolio") + p').textContent()) ?? ''
    // First number in the phrase is always the SHOWN count, whether the phrase is
    // "<N> companies" or the truncated "<N> of <M> companies" form.
    const match = headerText.match(/(\d+)(?:\s+of\s+\d+)?\s+companies/)
    expect(match, `header count text did not contain a "<N> companies" phrase: "${headerText}"`).not.toBeNull()
    expect(Number(match![1]), `header count must equal the ${rowCount} rows actually rendered under this filter`).toBe(rowCount)
  }

  await assertCountMatchesRenderedRows()

  await page.getByRole('button', { name: 'Active' }).click()
  await expect(page.locator('.pf-list-row').filter({ hasText: archivedEntity.name })).not.toBeVisible()
  await assertCountMatchesRenderedRows()

  await page.getByRole('button', { name: 'Archived' }).click()
  await expect(page.locator('.pf-list-row').filter({ hasText: activeEntity.name })).not.toBeVisible()
  await assertCountMatchesRenderedRows()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- Archive/restore, per row (BUG-01-12) -------------------------------------------

test('archive-restore: a client can be archived and restored from the row', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-14 portfolio archive-restore ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await goToClients(page)

  const row = page.locator('.pf-list-row').filter({ hasText: entity.name })
  await expect(row).toBeVisible()
  await expect(row.getByText('ACTIVE', { exact: true })).toBeVisible()
  const action = archiveAction(row)

  await action.click() // arm
  const offboardResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith(`/api/portfolio/v1/entities/${entity.id}/offboard`),
  )
  await action.click() // confirm
  await offboardResp
  await expect(row.getByText('ARCHIVED', { exact: true })).toBeVisible()

  await action.click() // arm
  const onboardResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith(`/api/portfolio/v1/entities/${entity.id}/onboard`),
  )
  await action.click() // confirm
  await onboardResp
  await expect(row.getByText('ACTIVE', { exact: true })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// A 409 is not organically reachable through the UI: every successful confirm refetches
// and flips the row, so the next arm always reads the opposite action. Reached instead by
// racing an out-of-band API call between this row's arm and confirm clicks, so the UI's
// own snapshot goes stale before the confirm fires.
test('archive-restore: a redundant transition surfaces the 409', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-14 portfolio archive-409 ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await goToClients(page)

  const row = page.locator('.pf-list-row').filter({ hasText: entity.name })
  await expect(row).toBeVisible()
  const action = archiveAction(row)

  await action.click() // arm, through the UI

  // Out-of-band: archive the same entity via the API seam, bypassing this browser.
  await offboardEntity(token, entity.id)

  const conflictResp = page.waitForResponse(
    (r) =>
      r.request().method() === 'POST' &&
      new URL(r.url()).pathname.endsWith(`/api/portfolio/v1/entities/${entity.id}/offboard`) &&
      r.status() === 409,
  )
  await action.click() // confirm, on the now-stale row
  await conflictResp

  await expect(row.getByText('redundant transition')).toBeVisible()
  // Not reported as changed: the row never refetches on failure, so the pre-race pill stands.
  await expect(row.getByText('ACTIVE', { exact: true })).toBeVisible()

  // Chromium logs the deliberate 409 network response as a console error; only that one is expected.
  const unexpectedErrors = errors.filter((e) => !e.includes('status of 409'))
  expect(unexpectedErrors, `console errors on the app:\n${unexpectedErrors.join('\n')}`).toEqual([])
})

test('archive-restore: archiving the open client leaves the switcher and the table in agreement', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-14 portfolio archive-open ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await selectEntity(page, entity.name)

  await goToClients(page)
  const row = page.locator('.pf-list-row').filter({ hasText: entity.name })
  await expect(row).toBeVisible()
  const action = archiveAction(row)

  await action.click() // arm
  const offboardResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith(`/api/portfolio/v1/entities/${entity.id}/offboard`),
  )
  await action.click() // confirm
  await offboardResp

  await expect(row.getByText('ARCHIVED', { exact: true })).toBeVisible()

  // Still in that workspace -- the switcher trigger keeps naming it, nothing auto-switched.
  await expect(page.getByTestId('company-switcher')).toContainText(entity.name)

  // The switcher dropdown carries no status pill (Sidebar.tsx) -- "agree" here means it
  // still LISTS the entity, not that it shows a matching ARCHIVED badge.
  await page.getByTestId('company-switcher').click()
  await expect(page.getByTestId('company-switcher-option').filter({ hasText: entity.name })).toBeVisible()

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('archive-restore: the row action does not open the edit modal', async ({ page }) => {
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `M4-14 portfolio archive-noclick ${Date.now()}`, tin: freshTin() })

  await signInFirm(page)
  await goToClients(page)

  const row = page.locator('.pf-list-row').filter({ hasText: entity.name })
  await expect(row).toBeVisible()
  const action = archiveAction(row)

  await action.click() // first click -- must arm, not open the modal
  await expect(page.getByRole('dialog')).not.toBeVisible()

  // Proves the first click actually armed it (not just "did nothing"): the second click
  // confirms and fires the offboard POST.
  const offboardResp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith(`/api/portfolio/v1/entities/${entity.id}/offboard`),
  )
  await action.click()
  await offboardResp

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
