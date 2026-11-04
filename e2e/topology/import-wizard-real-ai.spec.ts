// Wizard steps that only the real AI answers. Runs on the nightly only: PR previews run the fake AI.
import { test, expect } from '@playwright/test'
import { login, createEntity, createImportBatchFromCsv } from '../api/client'
import { freshTin } from '../api/fixtures'
import { PERF_HEADER, PERF_MAPPING } from '../importFixtures'
import { signInAs } from '../personaSession'
import { nightlyTarget } from '../targets'
import { collectErrors, selectEntity, shardPersonas } from './importWizardShared'

const { SHARD, PERSONAS } = shardPersonas('import-wizard-real-ai.spec.ts')

// Header of e2e/fixtures/bulk/layout-b-terminal.csv and its mapping.
const LAYOUT_Y_HEADER = 'Ref,Date,TIN,Customer,CCY,Net,Tax,Gross,Line,Units,Rate'
const LAYOUT_Y_MAPPING: Record<string, string> = {
  invoice_number: 'Ref',
  issue_date: 'Date',
  buyer_tin: 'TIN',
  buyer_name: 'Customer',
  currency: 'CCY',
  subtotal: 'Net',
  vat: 'Tax',
  total: 'Gross',
  line_description: 'Line',
  line_quantity: 'Units',
  line_unit_price: 'Rate',
}

function oneInvoiceCsv(header: string, invoiceNumber: string): string {
  return `${header}\n${invoiceNumber},2026-01-15,87654321-0002,Run Buyer,NGN,1000.00,75.00,1075.00,Line item,10,100.00\n`
}

const POST_SUFFIX = {
  suggest: '/api/invoice/v1/imports/suggest-mapping',
  check: '/api/invoice/v1/imports/check-mapping',
  import: '/api/invoice/v1/imports',
}

test.describe('the wizard on the real AI', () => {
  test.skip(!nightlyTarget(), 'nightly only: PR previews run the fake AI')
  test.use({ viewport: { width: 1440, height: 900 } })

  test.beforeAll(async () => {
    expect(test.info().project.name, 'import-wizard-real-ai.spec.ts belongs to the import-wizard-2 shard').toBe('import-wizard-2')
  })

  test('multi-file run: six picked, five kept, two layouts grouped, one file split out, one review URL', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)
    const stamp = Date.now()

    const aiPosts: string[] = []
    const importAnswers: { status: number; id: unknown }[] = []
    page.on('request', (r) => {
      if (r.method() !== 'POST') return
      const path = new URL(r.url()).pathname
      if (path.endsWith(POST_SUFFIX.suggest) || path.endsWith(POST_SUFFIX.check)) aiPosts.push(path)
    })
    page.on('response', async (r) => {
      if (r.request().method() !== 'POST' || !new URL(r.url()).pathname.endsWith(POST_SUFFIX.import)) return
      const body = (await r.json()) as { id?: unknown }
      importAnswers.push({ status: r.status(), id: body.id })
    })

    // Seed one saved mapping per layout, so the Map step restores both and asks no AI.
    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: `multi-file run ${stamp}`, tin: freshTin() })
    const seedX = await createImportBatchFromCsv(token, entity.id, {
      csv: oneInvoiceCsv(PERF_HEADER, `INV-RUN-${stamp}-SEED-X`),
      filename: 'seed-x.csv',
      mapping: PERF_MAPPING,
    })
    const seedY = await createImportBatchFromCsv(token, entity.id, {
      csv: oneInvoiceCsv(LAYOUT_Y_HEADER, `INV-RUN-${stamp}-SEED-Y`),
      filename: 'seed-y.csv',
      mapping: LAYOUT_Y_MAPPING,
    })
    expect(seedX.ready_invoices, 'layout X seed files one invoice').toBe(1)
    expect(seedY.ready_invoices, 'layout Y seed files one invoice').toBe(1)

    await signInAs(page, 'firm', { tenantId: SHARD.a.id })
    await selectEntity(page, entity.name)
    await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

    const csvFile = (name: string, header: string, n: number) => ({
      name,
      mimeType: 'text/csv',
      buffer: Buffer.from(oneInvoiceCsv(header, `INV-RUN-${stamp}-${n}`)),
    })
    const fileRow = (name: string) => page.locator('ul li').filter({ hasText: name })
    const fileNames = page.locator('ul li div.mono')

    await page.locator('input[type="file"]#pf-import-file').setInputFiles([
      csvFile('run-x-1.csv', PERF_HEADER, 1),
      csvFile('run-x-2.csv', PERF_HEADER, 2),
      csvFile('run-y-1.csv', LAYOUT_Y_HEADER, 3),
      csvFile('run-x-3.csv', PERF_HEADER, 4),
      csvFile('run-x-4.csv', PERF_HEADER, 5),
      csvFile('run-x-5.csv', PERF_HEADER, 6),
    ])
    await expect(page.getByText('A run accepts at most 5 files — 1 file was not added.', { exact: true })).toBeVisible()
    await expect(fileNames, 'the first five, in pick order').toHaveText(['run-x-1.csv', 'run-x-2.csv', 'run-y-1.csv', 'run-x-3.csv', 'run-x-4.csv'])

    await fileRow('run-x-3.csv').getByRole('button', { name: 'Remove' }).click()
    await expect(fileNames).toHaveText(['run-x-1.csv', 'run-x-2.csv', 'run-y-1.csv', 'run-x-4.csv'])
    await fileRow('run-x-4.csv').getByRole('button', { name: 'Remove' }).click()
    await expect(fileNames).toHaveText(['run-x-1.csv', 'run-x-2.csv', 'run-y-1.csv'])

    await page.getByRole('button', { name: 'Read columns' }).click()

    const group = (i: number, n: number) => page.getByText(`GROUP ${i} OF ${n}`, { exact: true })
    const covers = (sentence: string) => page.getByText(sentence, { exact: true })

    await expect(group(1, 2)).toBeVisible({ timeout: 60_000 })
    await expect(covers('This mapping applies to 2 files: run-x-1.csv and run-x-2.csv.')).toBeVisible()
    await expect(page.getByTestId('map-restored-notice')).toBeVisible()

    await page.getByRole('button', { name: 'Map run-x-2.csv separately' }).click()
    await expect(group(1, 3)).toBeVisible()
    await expect(covers('This mapping applies to 1 file: run-x-1.csv.')).toBeVisible()

    await page.getByRole('button', { name: 'Continue to next file' }).click()
    await expect(group(2, 3)).toBeVisible()
    await expect(covers('This mapping applies to 1 file: run-y-1.csv.')).toBeVisible()

    await page.getByRole('button', { name: 'Continue to next file' }).click()
    await expect(group(3, 3)).toBeVisible()
    await expect(covers('This mapping applies to 1 file: run-x-2.csv.')).toBeVisible()

    await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
    await expect(page.getByText('Importing 3 files', { exact: true })).toBeVisible({ timeout: 60_000 })
    for (const name of ['run-x-1.csv', 'run-x-2.csv', 'run-y-1.csv']) await expect(page.getByText(name, { exact: true })).toBeVisible()

    await expect(page.getByRole('heading', { name: '3 invoices imported' })).toBeVisible({ timeout: 60_000 })
    await expect.poll(() => importAnswers.length, { message: 'three import POSTs answered' }).toBe(3)
    expect(importAnswers.map((a) => a.status)).toEqual([201, 201, 201])
    const answeredIds = importAnswers.map((a) => a.id)
    for (const id of answeredIds) expect(typeof id, 'each import answers an id').toBe('string')

    const match = new URL(page.url()).pathname.match(/^\/imports\/([^/]+)\/review$/)
    expect(match, `the page lands on the batch review: ${page.url()}`).not.toBeNull()
    expect(new Set(decodeURIComponent(match![1]).split(',')), 'the review URL holds the three answered ids').toEqual(new Set(answeredIds as string[]))

    expect(aiPosts, 'restored mappings ask neither suggest-mapping nor check-mapping').toEqual([])
    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })
})
