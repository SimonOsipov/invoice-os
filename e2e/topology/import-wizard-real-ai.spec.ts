// Wizard steps that only the real AI answers. Runs on the nightly only: PR previews run the fake AI.
import { test, expect } from '@playwright/test'
import { login, createEntity, createImportBatchFromCsv, listInvoices } from '../api/client'
import { freshTin } from '../api/fixtures'
import { PERF_HEADER, PERF_MAPPING } from '../importFixtures'
import { signInAs } from '../personaSession'
import { nightlyTarget } from '../targets'
import { collectErrors, extractOneDocument, openExtractionReview, selectEntity, shardPersonas, uniqueHonestReceiptPdfBytes } from './importWizardShared'

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

// copy: the keys of DOCUMENT_TYPE_NOTICE (frontend/app/src/lib/extractionReview.ts); transcribed, not imported.
const NOTICE_TYPES = ['receipt', 'proforma', 'quotation', 'delivery note', 'statement', 'purchase order']

// copy: CHANGED_LABEL.typed (extractionReview.ts); transcribed, not imported.
const TYPED_LABEL = 'YOU CHANGED THIS'
const TYPEABLE_BUYER_FIELDS = ['buyer_name', 'buyer_street', 'buyer_city']

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

  test("AI-suggested mapping: a first import opens with the AI's placements, the checked doubts unplaced, and Import files it", async ({ page }) => {
    test.setTimeout(240_000)
    const errors = collectErrors(page)
    const stamp = Date.now()

    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: `AI suggestion ${stamp}`, tin: freshTin() })

    const suggestAnswers: { status: number; body: { source: string; columns: string[]; mapping: Record<string, string> } }[] = []
    const checkRequests: { mapping: Record<string, string> }[] = []
    const checkAnswers: { status: number; body: { doubted: unknown } }[] = []
    page.on('request', (r) => {
      if (r.method() === 'POST' && new URL(r.url()).pathname.endsWith(POST_SUFFIX.check)) checkRequests.push(JSON.parse(r.postData() ?? '{}'))
    })
    page.on('response', async (r) => {
      if (r.request().method() !== 'POST') return
      const path = new URL(r.url()).pathname
      if (path.endsWith(POST_SUFFIX.suggest)) suggestAnswers.push({ status: r.status(), body: await r.json() })
      if (path.endsWith(POST_SUFFIX.check)) checkAnswers.push({ status: r.status(), body: await r.json() })
    })

    await signInAs(page, 'firm', { tenantId: SHARD.a.id })
    await selectEntity(page, entity.name)
    await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
    await page.locator('input[type="file"]#pf-import-file').setInputFiles({
      name: 'ai-suggest.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(oneInvoiceCsv(PERF_HEADER, `INV-AISUGGEST-${stamp}`)),
    })
    await page.getByRole('button', { name: 'Read columns' }).click()

    // The Map step renders only after the check has answered.
    await expect(page.getByText('Map fields to columns ·', { exact: false })).toBeVisible({ timeout: 120_000 })

    expect(suggestAnswers, 'one suggest-mapping answer').toHaveLength(1)
    expect(suggestAnswers[0].status).toBe(200)
    const suggest = suggestAnswers[0].body
    // Lane control: the fake AI answers source "none".
    expect(suggest.source, 'control: this run must have reached the AI path').toBe('ai')
    const inv = suggest.mapping.invoice_number
    expect(typeof inv, 'control: the AI placed invoice_number').toBe('string')
    expect(suggest.columns, 'the placed column is one of the answer columns').toContain(inv)

    expect(checkRequests, 'one check-mapping request').toHaveLength(1)
    expect(checkAnswers, 'one check-mapping answer').toHaveLength(1)
    expect(checkAnswers[0].status).toBe(200)
    const doubtedRaw = checkAnswers[0].body.doubted
    expect(Array.isArray(doubtedRaw), 'doubted is an array').toBe(true)
    const doubted = doubtedRaw as unknown[]
    for (const d of doubted) expect(typeof d, 'each doubted entry is a string').toBe('string')
    const doubtedFields = doubted as string[]
    if (doubtedFields.length === 0) test.info().annotations.push({ type: 'no-doubt', description: 'real Jev doubted no placement' })

    const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const columns = page.getByTestId('map-column')
    const columnOf = (header: string) => columns.filter({ has: page.locator('div.mono', { hasText: new RegExp(`^${escape(header)}$`) }) })
    const chipOf = (scope: typeof columns, field: string) => scope.locator('span[draggable] .mono', { hasText: new RegExp(`^${escape(field)}$`, 'i') })

    // Doubted fields open unplaced; every other checked field sits on its checked header.
    for (const field of doubtedFields) await expect(chipOf(columns, field), `doubted ${field} opens unplaced`).toHaveCount(0)
    for (const [field, header] of Object.entries(checkRequests[0].mapping)) {
      if (doubtedFields.includes(field)) continue
      await expect(chipOf(columnOf(header), field), `${field} opens placed on ${header}`).toHaveCount(1)
    }

    const invColumn = columnOf(inv)
    const badge = invColumn.getByTestId('map-suggested-badge')
    const importBtn = page.getByRole('button', { name: /^Import \d+ rows$/ })
    if (!doubtedFields.includes('invoice_number')) {
      await expect(chipOf(invColumn, 'invoice_number'), 'invoice_number opens placed').toHaveCount(1)
      await expect(badge, 'one SUGGESTED badge on the column').toHaveCount(1)
      await expect(badge).toHaveText('SUGGESTED')
      await expect(importBtn, 'the suggestion alone arms Import').toBeEnabled()
    } else {
      await expect(chipOf(invColumn, 'invoice_number'), 'doubted invoice_number opens unplaced').toHaveCount(0)
      await expect(badge).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Map invoice number to continue' })).toBeVisible()
      await page.getByRole('button', { name: /^invoice_number\s*\*?$/i }).click()
      await invColumn.click()
      await expect(chipOf(invColumn, 'invoice_number')).toHaveCount(1)
      await expect(badge, 'the suggested snapshot returns the badge').toHaveCount(1)
      await expect(badge).toHaveText('SUGGESTED')
      await expect(importBtn).toBeEnabled()
      test.info().annotations.push({ type: 'doubted-invoice-number', description: 'real Jev doubted invoice_number; placed by hand' })
    }

    const importResp = page.waitForResponse((r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith(POST_SUFFIX.import), { timeout: 60_000 })
    await importBtn.click()
    const imported = await importResp
    expect(imported.status()).toBe(201)
    expect(((await imported.json()) as { rows_total: number }).rows_total).toBe(1)

    const { invoices } = await listInvoices(token, { entity_id: entity.id })
    expect(invoices, 'the fresh entity holds exactly the one imported invoice').toHaveLength(1)
    await expect(page).toHaveURL(new RegExp(`/invoices/${invoices[0].id}$`), { timeout: 60_000 })
    await expect(page.getByTestId('invoice-detail')).toBeVisible()

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('not a tax invoice: a receipt is typed as a non-invoice, the banner names it, and it files', async ({ page }) => {
    test.setTimeout(300_000)
    const errors = collectErrors(page)

    // extractOneDocument waits on invoice-detail: the import filed.
    await extractOneDocument(page, 'not a tax invoice', { name: 'honest_receipt_invoice.pdf', buffer: uniqueHonestReceiptPdfBytes() })
    await expect(page.getByTestId('invoice-detail')).toBeVisible()

    const detail = await openExtractionReview(page)
    const type = detail.document_type
    expect(type, 'real Jev read the receipt as a tax invoice').not.toBeNull()
    expect(NOTICE_TYPES, `the receipt was read as "${type}", not a non-invoice type that carries a notice`).toContain(type)

    const banner = page.getByTestId('extraction-document-type')
    await expect(banner).toBeVisible()
    await expect(banner).toContainText(type as string)
    await expect(banner).toContainText('not a tax invoice')

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('typed correction: one POST, and the field shows YOU CHANGED THIS', async ({ page }) => {
    test.setTimeout(300_000)
    const errors = collectErrors(page)
    const typedValue = `Typed buyer ${Date.now()}`

    await extractOneDocument(page, 'typed correction')
    const detail = await openExtractionReview(page)

    // A field with alternatives renders chips, not an input; the real read decides which.
    const field = TYPEABLE_BUYER_FIELDS.find((name) => {
      const f = detail.fields.find((x) => x.name === name)
      return f !== undefined && !(f.reason === 'ambiguous' && f.alternatives.length > 0)
    })
    expect(field, `precondition: none of ${TYPEABLE_BUYER_FIELDS.join(', ')} renders an input`).toBeDefined()
    const name = field as string

    const posts: { path: string; status: number; body: { method?: string; value?: string } }[] = []
    page.on('response', (r) => {
      const req = r.request()
      if (req.method() !== 'POST') return
      const path = new URL(r.url()).pathname
      if (path.endsWith('/corrections')) posts.push({ path, status: r.status(), body: req.postDataJSON() ?? {} })
    })

    const input = page.getByTestId(`extraction-input-${name}`)
    const save = page.getByTestId('extraction-save')
    await input.fill(typedValue)
    await expect(save, 'a typed value left Save disabled').toBeEnabled()

    await Promise.all([
      page.waitForResponse(
        (r) => r.request().method() === 'GET' && /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
        { timeout: 120_000 },
      ),
      save.click(),
    ])

    expect(posts, 'exactly one corrections POST, counted after the re-read').toHaveLength(1)
    expect(posts[0].path).toMatch(new RegExp(`/api/submission/v1/extractions/[0-9a-fA-F-]{36}/fields/${name}/corrections$`))
    expect(posts[0].status, 'the typed correction was refused').toBe(201)
    expect(posts[0].body.method).toBe('typed')
    expect(posts[0].body.value).toBe(typedValue)

    await expect(page.getByTestId(`extraction-marker-${name}`)).toBeVisible()
    await expect(page.getByTestId(`extraction-field-${name}`)).toContainText(TYPED_LABEL)
    await expect(input, 'the settled field holds the typed value').toHaveValue(typedValue)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })
})
