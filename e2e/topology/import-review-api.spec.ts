// The import review table on a batch minted through the API, with no AI.
import { test, expect, type Page, type Request, type Response } from '@playwright/test'
import { login, createEntity, createImportBatchFromCsv, type ImportBatchResult } from '../api/client'
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

type PageBody = { invoices: { id: string; invoice_number: string; kept_as_is_at: string | null }[]; pagination: { limit: number; offset: number; total: number } }

// Mints a batch and opens its review. The listeners and waits go up before signInAs, which loads
// the review itself: no reload, so no first-load request can be mistaken for a later one.
async function openReviewedBatch(page: Page, name: string, csv: string, filename: string) {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `NCOV-01 ${name} ${Date.now()}`, tin: freshTin() })
  const batch: ImportBatchResult = await createImportBatchFromCsv(token, entity.id, { csv, filename, mapping: PERF_MAPPING })
  const id = batch.id

  let summaryRequests = 0
  page.on('request', (r) => {
    if (isRequest(r, id, SUMMARY)) summaryRequests++
  })
  const [, tableResp, summaryResp] = await Promise.all([
    signInAs(page, 'firm', { tenantId: SHARD.a.id, path: `/imports/${id}/review` }),
    page.waitForResponse(tablePage(id), { timeout: 120_000 }),
    page.waitForResponse((r) => isRequest(r.request(), id, SUMMARY), { timeout: 120_000 }),
  ])
  await expect(page.getByRole('heading', { level: 2, name: `${batch.ready_invoices} invoices imported` })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('review-table')).toBeVisible()
  return {
    id,
    batch,
    firstPage: (await tableResp.json()) as PageBody,
    summary: (await summaryResp.json()) as { rules: { rule_key: string; invoices: number }[] },
    summaryRequests: () => summaryRequests,
  }
}

function violateId(firstPage: PageBody): string {
  const invoiceId = firstPage.invoices.find((r) => r.invoice_number === 'INV-UI-MIX-VIOLATE')?.id
  expect(invoiceId, 'the table page lists the VIOLATE invoice').toBeTruthy()
  return invoiceId!
}

test.describe('NCOV-01 import review table, API-minted batch', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('review table: filter pills, the failing-rules rail and search re-query the server', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    const { id, batch, firstPage, summary, summaryRequests } = await openReviewedBatch(page, 'filters', buildMixedCsv(), 'review-mixed.csv')
    // Fixture check: STRUCT never becomes an invoice; CLEAN is validated, VIOLATE is not.
    expect(batch.invoices_clean, 'the fixture has a validated invoice').toBeGreaterThanOrEqual(1)
    expect(batch.invoices_with_violations, 'the fixture has a failing invoice').toBeGreaterThanOrEqual(1)
    expect(batch.invoices_clean + batch.invoices_with_violations).toBe(batch.ready_invoices)
    expect(firstPage.pagination.total, 'the table total is the mint count').toBe(batch.ready_invoices)

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

    // Clearing the search is the next step; a duplicate of the first search would have fired by now.
    await Promise.all([
      page.waitForResponse((r) => isRequest(r.request(), id, INVOICES, { limit: '50', offset: '0' }) && (new URL(r.url()).searchParams.get('q') ?? '') === ''),
      page.getByTestId('review-search').fill(''),
    ])
    await expect(violate).toBeVisible()
    await expect(clean).toBeVisible()
    expect(searched, 'one fill sends one search request').toHaveLength(1)

    // The rail is batch-wide: it loaded once and no filter change re-asked it.
    expect(summaryRequests(), 'violation-summary fired once').toBe(1)
    expect(summary.rules).toContainEqual({ rule_key: 'vat-standard-rate', invoices: 1 })

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('review table: the pager reads the server total and Next asks for offset 50', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    // PAGE_SIZE + 5 invoices: a second page without the 500-invoice mint.
    const { id, batch, firstPage: first } = await openReviewedBatch(page, 'paging', buildPerfCsv(PAGE_SIZE + 5), 'review-perf.csv')
    const total = first.pagination.total
    expect(total, 'the pager total is the mint count').toBe(batch.ready_invoices)
    expect(total).toBeGreaterThanOrEqual(PAGE_SIZE + 1)
    expect(first.invoices).toHaveLength(Math.min(PAGE_SIZE, total))
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
    expect(page1).toHaveLength(first.invoices.length)

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

    const { id, firstPage } = await openReviewedBatch(page, 'fix', buildMixedCsv(), 'review-mixed.csv')
    const detailPath = `${INVOICES}/${violateId(firstPage)}`

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

    // Stay on the All pill: under the rule filter the fixed row would leave the view.
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

  test('review table: keep a failing invoice as-is, it leaves Needs a fix and stays disabled', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    const { id, firstPage } = await openReviewedBatch(page, 'keep', buildMixedCsv(), 'review-mixed.csv')
    const invoiceId = violateId(firstPage)
    expect(firstPage.invoices.find((r) => r.id === invoiceId)?.kept_as_is_at, 'not kept before Keep').toBeNull()
    const keepPath = `${INVOICES}/${invoiceId}/keep-as-is`

    // Counters read after the follow-up re-fetches have landed, so a late duplicate shows.
    let keeps = 0
    const keepBodies: unknown[] = []
    page.on('request', (r) => {
      if (r.method() === 'POST' && new URL(r.url()).pathname === keepPath) {
        keeps++
        keepBodies.push(r.postDataJSON())
      }
    })

    const violate = page.getByTestId('review-row').filter({ hasText: 'INV-UI-MIX-VIOLATE' })
    const reasonInput = page.getByTestId('review-keep-reason')
    const keep = page.getByTestId('review-keep')
    await expect(violate.getByTestId('review-verdict')).toContainText('RULES FAILED')
    await violate.click()
    await expect(reasonInput).toBeVisible()

    // Blank and whitespace-only reasons leave Keep disabled.
    await expect(keep).toBeDisabled()
    await reasonInput.fill('   ')
    await expect(keep).toBeDisabled()
    expect(keeps, 'no keep-as-is request for a blank reason').toBe(0)

    // The SPA trims the reason before it sends it.
    const reason = 'Approved by finance, one-off exception.'
    await reasonInput.fill(`  ${reason}  `)
    await expect(keep).toBeEnabled()
    const [kept, afterKeep] = await Promise.all([
      page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname === keepPath),
      page.waitForResponse(tablePage(id)),
      keep.click(),
    ])
    expect(kept.ok(), 'Keep as-is answers OK').toBe(true)
    // internal/invoice/invoice.go Invoice.KeptAsIsAt (json "kept_as_is_at"); the row is unselectable before Keep too, so the wire is the proof.
    const keptRow = ((await afterKeep.json()) as PageBody).invoices.find((r) => r.id === invoiceId)
    expect(keptRow?.kept_as_is_at, 'the table re-read carries the keep').toBeTruthy()
    // internal/invoice/handlers.go keepAsIsRequest (json "reason").
    expect(keepBodies).toEqual([{ reason }])
    await expect(page.getByTestId('review-kept-banner')).toContainText(reason)
    await expect(violate.getByTestId('review-verdict')).toContainText('KEPT')
    expect(keeps, 'Keep sends one POST').toBe(1)

    // Kept leaves "Needs a fix" but stays in "All", unselectable.
    await click(page, id, page.getByTestId('review-filter-pill').filter({ hasText: /^Needs a fix/ }), { needs_fix: 'true' })
    await expect(violate).toHaveCount(0)
    await click(page, id, page.getByTestId('review-filter-pill').filter({ hasText: /^All/ }), { needs_fix: null, status: null })
    await expect(violate).toBeVisible()
    const select = violate.getByTestId('review-select')
    await expect(select).toBeVisible()
    await expect(select).toBeDisabled()
    expect(keeps, 'the filter changes sent no further keep-as-is').toBe(1)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })
})
