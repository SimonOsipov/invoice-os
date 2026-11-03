// The import review table on a batch minted through the API, with no AI (NCOV-01).
import { test, expect, type Page, type Request, type Response } from '@playwright/test'
import { login, createEntity, createImportBatchFromCsv } from '../api/client'
import { freshTin } from '../api/fixtures'
import { buildMixedCsv, PERF_MAPPING } from '../importFixtures'
import { signInAs } from '../personaSession'
import { collectErrors, shardPersonas } from './importWizardShared'

const { SHARD, PERSONAS } = shardPersonas('import-review-api.spec.ts')

test.beforeAll(async () => {
  expect(test.info().project.name, 'import-review-api.spec.ts belongs to the import-wizard-2 shard').toBe('import-wizard-2')
})

const INVOICES = '/api/invoice/v1/invoices'
const SUMMARY = '/api/invoice/v1/invoices/violation-summary'

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
})
