// M4-14-03 (task-210): the portfolio capability spec -- edit-client (Gap 2), create-client
// (day30 AC-3 preserved), status-pill (day30 AC-2 preserved), and client health pill
// (Gap 3, steady-state). ClientsView/EntityFormModal carry no data-testid
// ([no-testids-on-portfolio-dashboard], grep-verified) -- every selector below is by
// role/exact-text/CSS-class, the same idiom day30.spec.ts/topology.spec.ts already used
// for this surface before this story split them out.
//
// OFFBOARD UI IS OUT OF SCOPE ([gap-2-edit-covered / offboard-deferred]): EntityFormModal's
// edit mode ships only Cancel/Save (no archive/offboard button -- grep-verified against
// EntityFormModal.tsx). offboardEntity() (../api/client, POST .../offboard) is used below
// ONLY as an API-seam seed to produce an ARCHIVED row for the status-pill read -- it is
// never exercised through the UI. Building an offboard control is production UI, which is
// Out of Scope for this test-and-docs-only story.
import { test, expect, type Page } from '@playwright/test'
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
  const res = await page.goto(APP_URL)
  expect(res, `no response from ${APP_URL}`).toBeTruthy()
  expect(res!.ok(), `${APP_URL} returned HTTP ${res!.status()}`).toBeTruthy()
  await page.getByRole('button', { name: new RegExp(FIRM_PERSONA.buttonName) }).click()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
}

// goToClients(): the Clients sidebar nav button (glyphs.tsx's NAV_CLIENTS, label
// "Clients") -- no data-testid on this surface, so a plain role/name click, mirroring
// invoice-surfaces.spec.ts's own inline `page.getByRole('button', { name: /Clients/ })`
// (Day-60 arc). Extracted here since every test in this file drives Clients.
async function goToClients(page: Page): Promise<void> {
  await page.getByRole('button', { name: /Clients/ }).click()
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
// zero invoices ever created has no row in the rollup's `clients` (INNER JOIN) -> "no
// invoices yet"; an entity with exactly one needs_attention invoice -> "1 NEEDS ATTENTION".
// The third health state (ALL CLEAR -- a validated entity with zero violations) is NOT
// covered here: the story's AC #4 names only these two cases, and invoice-surfaces.spec.ts's
// Day-60 arc already exercises ALL CLEAR as its own post-fix assertion. Adding it here would
// be scope creep on a test-and-docs-only subtask.
test('health-pill: a fresh entity with a needs-attention invoice reads "1 NEEDS ATTENTION"; a fresh entity with no invoices reads "no invoices yet"', async ({
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
  await expect(attnRow).toContainText('1 NEEDS ATTENTION')
  await expect(emptyRow).toContainText('no invoices yet')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})
