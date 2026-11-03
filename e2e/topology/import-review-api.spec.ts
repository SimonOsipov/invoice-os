// The import review table on a batch minted through the API, with no AI (NCOV-01).
import { test, expect, type Page, type Request, type Response } from '@playwright/test'
import { login, createEntity, createImportBatchFromCsv } from '../api/client'
import { freshTin } from '../api/fixtures'
import { buildMixedCsv, buildPerfCsv, PERF_MAPPING } from '../importFixtures'
import { signInAs } from '../personaSession'
import { collectErrors, shardPersonas } from './importWizardShared'

const { SHARD, PERSONAS } = shardPersonas('import-review-api.spec.ts')

test.beforeAll(async () => {
  expect(test.info().project.name, 'import-review-api.spec.ts belongs to the import-wizard-2 shard').toBe('import-wizard-2')
})

const INVOICES = '/api/invoice/v1/invoices'
const SUMMARY = '/api/invoice/v1/invoices/violation-summary'
const PAGE_SIZE = 50 // REVIEW_PAGE_SIZE, frontend/app/src/lib/reviewBatch.ts

// Params map to the exact value, or null for "absent". import_batch_id must be exactly the batch.
function isRequest(req: Request, batchId: string, path: string, params: Record<string, string | null> = {}): boolean {
  const url = new URL(req.url())
  if (req.method() !== 'GET' || url.pathname !== path) return false
  if (url.searchParams.getAll('import_batch_id').join() !== batchId) return false
  return Object.entries(params).every(([k, v]) => (v === null ? !url.searchParams.has(k) : url.searchParams.get(k) === v))
}

// The table's page query (limit=50), not the shell's limit=1 count queries.
function tablePage(batchId: string, params: Record<string, string | null> = {}) {
  return (r: Response) => isRequest(r.request(), batchId, INVOICES, { limit: '50', offset: '0', ...params })
}

async function click(page: Page, batchId: string, target: ReturnType<Page['locator']>, params: Record<string, string | null>) {
  await Promise.all([page.waitForResponse(tablePage(batchId, params)), target.click()])
}

test.describe('NCOV-01 import review table, API-minted batch', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('review table: filter pills, the failing-rules rail and search re-query the server', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: `NCOV-01 filters ${Date.now()}`, tin: freshTin() })
    const batch = await createImportBatchFromCsv(token, entity.id, { csv: buildMixedCsv(), filename: 'review-mixed.csv', mapping: PERF_MAPPING })
    // Fixture check: STRUCT never becomes an invoice; CLEAN is validated, VIOLATE is not.
    expect({ ready: batch.ready_invoices, clean: batch.invoices_clean, violating: batch.invoices_with_violations }).toEqual({
      ready: 2,
      clean: 1,
      violating: 1,
    })
    const id = batch.id

    await signInAs(page, 'firm', { tenantId: SHARD.a.id, path: `/imports/${id}/review` })

    // Counters and waits attach before the reload; signInAs loads through the landing hand-off.
    let summaryRequests = 0
    page.on('request', (r) => {
      if (isRequest(r, id, SUMMARY)) summaryRequests++
    })
    const [, , summaryResp] = await Promise.all([
      page.reload(),
      page.waitForResponse(tablePage(id)),
      page.waitForResponse((r) => isRequest(r.request(), id, SUMMARY)),
    ])
    await expect(page.getByRole('heading', { level: 2, name: '2 invoices imported' })).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId('review-table')).toBeVisible()

    const violate = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-VIOLATE' })
    const clean = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-CLEAN' })
    const pill = (label: RegExp) => page.getByTestId('review-filter-pill').filter({ hasText: label })
    await expect(violate).toBeVisible()
    await expect(clean).toBeVisible()

    // Needs a fix
    await click(page, id, pill(/^Needs a fix/), { needs_fix: 'true', status: null, rule_key: null, q: null })
    await expect(violate).toBeVisible()
    await expect(clean).toHaveCount(0)

    // Validated
    await click(page, id, pill(/^Validated/), { status: 'validated', needs_fix: null })
    await expect(clean).toBeVisible()
    await expect(violate).toHaveCount(0)

    // All
    await click(page, id, pill(/^All/), { status: null, needs_fix: null })
    await expect(violate).toBeVisible()
    await expect(clean).toBeVisible()

    // Rail pill, on then off
    const rail = page.getByTestId('review-rail-pill').filter({ hasText: 'vat-standard-rate' })
    await expect(rail).toBeVisible()
    await click(page, id, rail, { rule_key: 'vat-standard-rate' })
    await expect(rail).toHaveAttribute('aria-pressed', 'true')
    await expect(violate).toBeVisible()
    await expect(clean).toHaveCount(0)
    await click(page, id, rail, { rule_key: null })
    await expect(rail).toHaveAttribute('aria-pressed', 'false')
    await expect(violate).toBeVisible()
    await expect(clean).toBeVisible()

    // Search: one fill is one debounced request.
    const searched: string[] = []
    page.on('request', (r) => {
      if (isRequest(r, id, INVOICES, { limit: '50', q: 'MIX-CLEAN' })) searched.push(r.url())
    })
    await Promise.all([page.waitForResponse(tablePage(id, { q: 'MIX-CLEAN', rule_key: null })), page.getByTestId('review-search').fill('MIX-CLEAN')])
    await expect(clean).toBeVisible()
    await expect(violate).toHaveCount(0)
    expect(searched, 'one fill sends one search request').toHaveLength(1)

    // The rail is batch-wide: it loaded once and no filter change re-asked it.
    expect(summaryRequests, 'violation-summary fired once from the reload on').toBe(1)
    const summary = (await summaryResp.json()) as { rules: { rule_key: string; invoices: number }[] }
    expect(summary.rules).toContainEqual({ rule_key: 'vat-standard-rate', invoices: 1 })

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('review table: the pager reads the server total and Next asks for offset 50', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: `NCOV-01 paging ${Date.now()}`, tin: freshTin() })
    const batch = await createImportBatchFromCsv(token, entity.id, { csv: buildPerfCsv(), filename: 'review-perf.csv', mapping: PERF_MAPPING })
    const id = batch.id

    await signInAs(page, 'firm', { tenantId: SHARD.a.id, path: `/imports/${id}/review` })

    const [, firstResp] = await Promise.all([page.reload(), page.waitForResponse(tablePage(id))])
    await expect(page.getByRole('heading', { level: 2, name: /invoices imported/ })).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId('review-table')).toBeVisible()

    type PageBody = { invoices: { invoice_number: string }[]; pagination: { limit: number; offset: number; total: number } }
    const first = (await firstResp.json()) as PageBody
    const total = first.pagination.total
    expect(total, 'the pager total is the mint count').toBe(batch.ready_invoices)
    expect(total).toBeGreaterThanOrEqual(PAGE_SIZE + 1)
    const pages = Math.ceil(total / PAGE_SIZE)

    const pager = page.getByTestId('review-pager')
    const prev = pager.getByRole('button', { name: '← Previous' })
    const next = pager.getByRole('button', { name: 'Next →' })
    await expect(pager).toContainText(`SHOWING 1–${PAGE_SIZE} OF ${total}`)
    await expect(pager).toContainText(`PAGE 1 / ${pages}`)
    await expect(prev).toBeDisabled()
    await expect(next).toBeEnabled()
    const rowNumbers = async () => (await page.getByTestId('review-row').allTextContents()).map((t) => /INV-UI-\d+/.exec(t)?.[0] ?? t)
    const page1 = await rowNumbers()
    expect(page1).toHaveLength(PAGE_SIZE)

    const secondReq: string[] = []
    page.on('request', (r) => {
      if (isRequest(r, id, INVOICES, { limit: String(PAGE_SIZE), offset: String(PAGE_SIZE) })) secondReq.push(r.url())
    })
    const [secondResp] = await Promise.all([page.waitForResponse(tablePage(id, { offset: String(PAGE_SIZE) })), next.click()])
    const second = (await secondResp.json()) as PageBody
    expect(second.pagination.offset).toBe(PAGE_SIZE)
    expect(secondReq, 'Next sends one offset=50 request').toHaveLength(1)

    await expect(pager).toContainText(`SHOWING ${PAGE_SIZE + 1}–${Math.min(2 * PAGE_SIZE, total)} OF ${total}`)
    await expect(pager).toContainText(`PAGE 2 / ${pages}`)
    const page2 = await rowNumbers()
    expect(page2.length).toBeGreaterThan(0)
    expect(page2.filter((n) => page1.includes(n)), 'page 2 shares no invoice with page 1').toEqual([])

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('review table: fix the VAT inline, save once, re-validate once, the verdict moves', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: `NCOV-01 fix ${Date.now()}`, tin: freshTin() })
    const batch = await createImportBatchFromCsv(token, entity.id, { csv: buildMixedCsv(), filename: 'review-mixed.csv', mapping: PERF_MAPPING })
    const id = batch.id

    await signInAs(page, 'firm', { tenantId: SHARD.a.id, path: `/imports/${id}/review` })

    const [, firstResp] = await Promise.all([page.reload(), page.waitForResponse(tablePage(id))])
    await expect(page.getByRole('heading', { level: 2, name: '2 invoices imported' })).toBeVisible({ timeout: 60_000 })
    await expect(page.getByTestId('review-table')).toBeVisible()

    const rows = ((await firstResp.json()) as { invoices: { id: string; invoice_number: string }[] }).invoices
    const invoiceId = rows.find((r) => r.invoice_number === 'INV-UI-MIX-VIOLATE')?.id
    expect(invoiceId, 'the table page lists the VIOLATE invoice').toBeTruthy()
    const detailPath = `${INVOICES}/${invoiceId}`

    // Counters read after the follow-up re-fetches have landed, so a late duplicate shows.
    let patches = 0
    let validates = 0
    const patchBodies: unknown[] = []
    page.on('request', (r) => {
      const path = new URL(r.url()).pathname
      if (r.method() === 'PATCH' && path === detailPath) {
        patches++
        patchBodies.push(r.postDataJSON())
      }
      if (r.method() === 'POST' && path === `${detailPath}/validate`) validates++
    })
    const isDetailGet = (r: Response) => r.request().method() === 'GET' && new URL(r.url()).pathname === detailPath

    // Stay on the All pill (D8): under the rule filter the fixed row would leave the view.
    const violate = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-VIOLATE' })
    const fixCard = page.getByTestId('review-fix-card')
    const fixInput = page.getByTestId('review-fix-input')
    await expect(violate.getByTestId('review-verdict')).toContainText('RULES FAILED')
    await violate.click()
    await expect(fixCard).toContainText('vat-standard-rate')
    await expect(fixInput).toHaveValue('0.00')

    await fixInput.fill('75.00')
    const [patched] = await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'PATCH' && new URL(r.url()).pathname === detailPath),
      page.waitForResponse(isDetailGet),
      page.getByTestId('review-fix-save').click(),
    ])
    expect(patched.ok(), 'the fix is stored').toBe(true)
    // internal/invoice/invoice.go Invoice.VAT (json "vat").
    expect(patchBodies).toHaveLength(1)
    expect((patchBodies[0] as { vat?: string }).vat).toBe('75.00')
    expect(((await patched.json()) as { vat: string | null }).vat).toBe('75.00')
    await expect(fixInput, 'the re-fetched invoice shows the stored value').toHaveValue('75.00')
    expect(patches, 'Save sends one PATCH').toBe(1)

    const [validated] = await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === `${detailPath}/validate`),
      page.waitForResponse(tablePage(id)),
      page.getByTestId('review-revalidate').click(),
    ])
    expect(validated.ok(), 'Re-validate answers OK').toBe(true)
    await expect(violate.getByTestId('review-verdict')).toContainText('VALIDATED')
    await expect(violate.getByTestId('review-verdict')).not.toContainText('RULES FAILED')
    await expect(page.getByTestId('review-row-passing')).toBeVisible()
    expect(validates, 'Re-validate sends one POST').toBe(1)
    expect(patches, 'Re-validate sends no further PATCH').toBe(1)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })
})
