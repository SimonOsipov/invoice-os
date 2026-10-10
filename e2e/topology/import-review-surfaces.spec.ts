// RESKIN2-04: the v2 create, progress and review surfaces as the PR environment resolves them. Value reads and
// relationship sweeps; screenshots are attached for the reviewer and never asserted.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

import { test, expect, type Locator, type Page, type Request, type Response, type TestInfo } from '@playwright/test'
import { login, createEntity, createImportBatch, listInvoices, approveUntilClosed, firmApproverTokens, type ExtractionDetail, type Persona } from '../api/client'
import { ensureFirmPolicyActive } from '../api/contract-helpers'
import { freshTin } from '../api/fixtures'
import { approvalRun404Dropper, type Dropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, enclosesRect, rectsOverlap, settleAnimations, WIDE_WIDTHS, type Rect } from './layout'
import { GATEWAY_URL, shardTenants } from './targets'
import { signInAs } from '../personaSession'
import { buildAir07UnsteeredCsv, buildHeaderOnlyCsv, buildMixedCsv } from '../importFixtures'

const SHARD = shardTenants('import-review-surfaces.spec.ts')
const GATEWAY_ORIGIN = new URL(GATEWAY_URL).origin
const PERSONAS: { A: Persona } = { A: { ...SHARD.a, tenantId: SHARD.a.id } }

test.beforeAll(async () => {
  expect(test.info().project.name, 'import-review-surfaces.spec.ts belongs to the import-wizard shard').toBe('import-wizard')
  await ensureFirmPolicyActive(await login(PERSONAS.A))
})

// Copy of importWizardShared.ts's collectErrors.
function collectErrors(page: Page, extra?: Dropper): string[] {
  const errors: string[] = []
  const droppers = [approvalRun404Dropper(page), ...(extra ? [extra] : [])]
  page.on('console', (msg) => {
    if (msg.type() !== 'error') return
    if (droppers.some((drop) => drop(msg.text(), msg.location().url))) return
    errors.push(msg.text())
  })
  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`)
  })
  return errors
}

async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

const DOCUMENT_FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents')
const NATIVE_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'rich_invoice.pdf'))

// Fresh bytes per call: a trailing PDF comment moves the content hash, so per-tenant dedupe never reuses an earlier run.
function uniquePdfBytes(): Buffer {
  return Buffer.concat([NATIVE_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// A dispatched drop: addPickedFiles sees it without `accept`. `bytes` sizes the file; nothing dropped is uploaded.
async function dropFiles(page: Page, specs: { name: string; type: string; bytes?: number }[]): Promise<void> {
  await page.evaluate((list) => {
    const label = document.querySelector('label[for="pf-import-file"]')
    if (!label) throw new Error('dropzone label[for="pf-import-file"] not found')
    const dt = new DataTransfer()
    for (const spec of list) {
      const body: BlobPart[] = spec.bytes ? [new Uint8Array(spec.bytes)] : [`e2e ${spec.name}`]
      dt.items.add(new File(body, spec.name, { type: spec.type }))
    }
    label.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt }))
  }, specs)
}

// Two consecutive AGREEING reads, never one; returns the agreed read.
async function settledRead<T>(read: () => Promise<T>, label: string): Promise<T> {
  let previous = ''
  let previousValue!: T
  await expect
    .poll(
      async () => {
        const value = await read()
        const key = JSON.stringify(value)
        const stable = key === previous
        previous = key
        previousValue = value
        return stable
      },
      { message: `${label}: geometry never settled across two consecutive reads`, timeout: 15_000 },
    )
    .toBe(true)
  return previousValue
}

const ACCEPTED_LINE = 'ACCEPTED · CSV · XLSX · PDF · DOCX'

type Rgb = { r: number; g: number; b: number; a: number }

// Chrome serialises a resolved colour as rgb()/rgba(), or as color(srgb ...) for a colour mix.
function parseColor(raw: string): Rgb {
  const alpha = (a: string | undefined): number => (a === undefined ? 1 : a.endsWith('%') ? parseFloat(a) / 100 : parseFloat(a))
  const rgb = raw.match(/^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:\s*[,/]\s*([\d.]+%?))?\s*\)$/)
  if (rgb) return { r: +rgb[1], g: +rgb[2], b: +rgb[3], a: alpha(rgb[4]) }
  const srgb = raw.match(/^color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)(?:\s*\/\s*([\d.]+%?))?\s*\)$/)
  if (srgb) return { r: +srgb[1] * 255, g: +srgb[2] * 255, b: +srgb[3] * 255, a: alpha(srgb[4]) }
  throw new Error(`cannot parse the resolved colour ${JSON.stringify(raw)}`)
}

// WCAG 2.x contrast ratio of two opaque resolved colours.
function contrastRatio(a: string, b: string): number {
  const lum = (raw: string): number => {
    const { r, g, b: blue, a: alpha } = parseColor(raw)
    if (alpha < 1) throw new Error(`contrast needs an opaque colour, got ${raw}`)
    const lin = (c: number): number => {
      const s = c / 255
      return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
    }
    return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(blue)
  }
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const firstFamily = (raw: string): string => raw.split(',')[0].replace(/["']/g, '').trim()

const CORNERS = ['border-top-left-radius', 'border-top-right-radius', 'border-bottom-right-radius', 'border-bottom-left-radius']
const PROPS = [
  ...CORNERS,
  'border-top-width',
  'border-top-style',
  'border-top-color',
  'border-bottom-width',
  'border-bottom-style',
  'border-bottom-color',
  'box-shadow',
  'background-color',
  'background-image',
  'animation-duration',
  'color',
  'height',
  'opacity',
  'font-family',
  'font-weight',
]

type Read = { style: Record<string, string>; rect: Rect; text: string }

// Waits until a collection reaches its floor, so an empty list never satisfies the assertions over its items.
async function floor(loc: Locator, min: number, message: string): Promise<void> {
  await expect.poll(() => loc.count(), { message, timeout: 60_000 }).toBeGreaterThanOrEqual(min)
}

// Fonts settled and two frames painted, the targets' animations finished, then every value in one evaluate.
// `tokens` resolve through a probe placed in the first target's `.asc-app`, where the tokens are declared.
async function readAll(
  page: Page,
  locs: Record<string, Locator>,
  tokens: string[] = [],
): Promise<{ reads: Record<string, Read[]>; tokens: Record<string, string> }> {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
  })
  await settleAnimations(...Object.values(locs).map((l) => l.first()))
  const names = Object.keys(locs)
  const handleSets = await Promise.all(names.map((n) => locs[n].elementHandles()))
  try {
    return await page.evaluate(
      ({ names, handleSets, tokens, props }) => {
        const reads: Record<string, Read[]> = {}
        names.forEach((name, i) => {
          reads[name] = handleSets[i].map((node) => {
            const el = node as Element
            const cs = getComputedStyle(el)
            const r = el.getBoundingClientRect()
            return {
              style: Object.fromEntries(props.map((p) => [p, cs.getPropertyValue(p)])),
              rect: { x: r.x, y: r.y, width: r.width, height: r.height },
              text: el.textContent ?? '',
            }
          })
        })
        const resolved: Record<string, string> = {}
        if (tokens.length > 0) {
          const host = (handleSets.flat()[0] as Element | undefined)?.closest('.asc-app')
          for (const token of tokens) {
            if (!host || getComputedStyle(host).getPropertyValue(token).trim() === '') {
              throw new Error(`token ${token} is not declared on the .asc-app that holds the first target`)
            }
            const probe = document.createElement('span')
            probe.style.color = `var(${token})`
            host.appendChild(probe)
            resolved[token] = getComputedStyle(probe).color
            probe.remove()
          }
        }
        return { reads, tokens: resolved }
      },
      { names, handleSets, tokens, props: PROPS },
    )
  } finally {
    await Promise.all(handleSets.flat().map((h) => h.dispose()))
  }
}

function expectCorners(style: Record<string, string>, want: string, label: string): void {
  for (const corner of CORNERS) expect(style[corner], `${label}: ${corner}`).toBe(want)
}

// Runs `measure` at every WIDE_WIDTHS entry, widest first, and restores the entry viewport.
async function sweep<T extends object>(
  page: Page,
  label: string,
  measure: (width: number) => Promise<T>,
): Promise<(T & { width: number })[]> {
  const entry = page.viewportSize()
  const out: (T & { width: number })[] = []
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })
      out.push({ width, ...(await measure(width)) })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  expect(
    out.map((m) => m.width),
    `${label}: every WIDE_WIDTHS entry must be measured, widest first`,
  ).toEqual([...WIDE_WIDTHS])
  return out
}

async function attachJson(testInfo: TestInfo, name: string, body: unknown): Promise<void> {
  await testInfo.attach(`${name}.json`, { body: JSON.stringify(body, null, 2), contentType: 'application/json' })
}

async function shoot(page: Page, testInfo: TestInfo, file: string): Promise<void> {
  await testInfo.attach(file, { body: await page.screenshot(), contentType: 'image/png' })
}

// An extraction screenshot waits for the page image: the frame is visible before the bitmap paints.
async function shootExtraction(page: Page, testInfo: TestInfo, file: string): Promise<void> {
  await expect
    .poll(() => page.locator('[data-testid^="extraction-page-image-"]').first().evaluate((el) => (el as HTMLImageElement).complete && (el as HTMLImageElement).naturalWidth > 0), {
      message: 'the page image has loaded before the screenshot',
      timeout: 60_000,
    })
    .toBe(true)
  await shoot(page, testInfo, file)
}

// Records every request that could write, so a test can assert none left the page.
function recordWrites(page: Page): { writes: string[]; stop: () => void } {
  const writes: string[] = []
  const onRequest = (req: Request) => {
    if (req.method() === 'GET' || req.method() === 'OPTIONS') return
    const url = new URL(req.url())
    if (url.origin === GATEWAY_ORIGIN && url.pathname.startsWith('/api/')) writes.push(`${req.method()} ${url.pathname}`)
  }
  page.on('request', onRequest)
  return { writes, stop: () => page.off('request', onRequest) }
}

// A fresh entity, signed in and selected, on the wizard's upload step.
async function startWizard(page: Page, label: string): Promise<{ token: string; entityId: string }> {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `${label} ${Date.now()}`, tin: freshTin() })
  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  return { token, entityId: entity.id }
}

const isPreview = (r: Response) =>
  r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview')
const isImport = (r: Response) =>
  r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports')

// Picks a CSV and reads its columns, leaving the Map step open.
async function pickAndRead(page: Page, name: string, csv: string): Promise<void> {
  await page.locator('input[type="file"]#pf-import-file').setInputFiles({ name, mimeType: 'text/csv', buffer: Buffer.from(csv, 'utf8') })
  const preview = page.waitForResponse(isPreview, { timeout: 60_000 })
  await page.getByRole('button', { name: 'Read columns' }).click()
  await preview
}

// invoice_number and subtotal never auto-place on the mixed fixture, so they are click-placed.
async function placeMixedColumns(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()
}

async function clickImport(page: Page): Promise<void> {
  const importResp = page.waitForResponse(isImport, { timeout: 60_000 })
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await importResp
}

// Closes every open approval run of the entity over the API, so the validated rows become selectable.
async function approveOpenRunsForEntity(token: string, entityId: string): Promise<void> {
  const { invoices } = await listInvoices(token, { entity_id: entityId })
  const approverTokens = await firmApproverTokens(SHARD.a.id)
  await Promise.all(
    invoices.filter((inv) => inv.approval?.run_state === 'open').map((inv) => approveUntilClosed(inv.id, approverTokens)),
  )
}

// Each header cell's left edge equals the cell below it in every data row, and no two cells of a row overlap.
async function assertGridAligned(page: Page, headerLabel: string, label: string): Promise<{ rows: number }> {
  const head = page.getByText(headerLabel, { exact: true }).locator('xpath=..')
  await expect(head, `${label}: the grid header must render`).toBeVisible()
  const m = await settledRead(
    () =>
      head.evaluate((h) => {
        const rect = (el: Element) => {
          const r = el.getBoundingClientRect()
          return { x: r.x, y: r.y, width: r.width, height: r.height }
        }
        const rows = [...(h.parentElement as HTMLElement).children].filter(
          (c) => c !== h && getComputedStyle(c).display === 'grid' && c.children.length === h.children.length,
        )
        return { head: [...h.children].map(rect), rows: rows.map((row) => [...row.children].map(rect)) }
      }),
    label,
  )
  expect(m.rows.length, `${label}: the grid must hold at least one data row`).toBeGreaterThan(0)
  for (const [i, cells] of m.rows.entries()) {
    for (const [c, cell] of cells.entries()) {
      expect(Math.abs(cell.x - m.head[c].x), `${label}: row ${i} cell ${c} left edge vs its header`).toBeLessThanOrEqual(1)
      for (const other of cells.slice(c + 1)) {
        expect(rectsOverlap(cell, other), `${label}: two cells of row ${i} overlap`).toBe(false)
      }
    }
  }
  return { rows: m.rows.length }
}

// Copy of importWizardShared.ts's extractOneDocument: the document journey to the invoice detail, then the review screen.
async function extractOneDocument(
  page: Page,
  label: string,
  file: { name: string; buffer: Buffer } = { name: 'native_invoice.pdf', buffer: uniquePdfBytes() },
): Promise<void> {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `${label} ${Date.now()}`, tin: freshTin() })

  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: file.name, mimeType: 'application/pdf', buffer: file.buffer })
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  await expect(page.getByTestId('extraction-review'), 'the document run must land on its extraction review').toBeVisible({
    timeout: 240_000,
  })
  await expect
    .poll(() => new URL(page.url()).pathname, { message: 'the landing must be addressed at its own job path' })
    .toMatch(/^\/extraction\/[0-9a-fA-F-]{36}$/)
  await expect(page.getByTestId('extraction-open-invoice'), 'the review must offer its exit').toBeVisible({ timeout: 60_000 })
  await page.getByTestId('extraction-open-invoice').click()
  await expect(page.getByTestId('invoice-detail'), 'the exit must reach the real invoice detail').toBeVisible({ timeout: 60_000 })
}

// Opens the review screen and returns the 200 the SPA itself consumed.
async function openExtractionReview(page: Page): Promise<ExtractionDetail> {
  const [res] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 120_000 },
    ),
    page.getByTestId('open-extraction-review').click(),
  ])

  expect(res.status(), 'the review screen must read its detail over a 200').toBe(200)
  await expect(page.getByTestId('extraction-review'), 'the review screen must open').toBeVisible({ timeout: 60_000 })
  await expect(page.getByTestId('extraction-page-1'), 'the canvas must render at least one frame').toBeVisible({
    timeout: 60_000,
  })
  return (await res.json()) as ExtractionDetail
}

async function boxOf(page: Page, testid: string): Promise<Rect> {
  const box = await page.getByTestId(testid).boundingBox()
  expect(box, `${testid} did not render`).not.toBeNull()
  return box as Rect
}

// The CONTENT box plus scroll metrics: a border-box comparison passes a row that overflows its gutter.
function edgesOf(el: HTMLElement) {
  const r = el.getBoundingClientRect()
  const cs = getComputedStyle(el)
  return {
    left: r.left + parseFloat(cs.borderLeftWidth) + parseFloat(cs.paddingLeft),
    right: r.right - parseFloat(cs.borderRightWidth) - parseFloat(cs.paddingRight),
    outerLeft: r.left,
    outerRight: r.right,
    scrollWidth: el.scrollWidth,
    clientWidth: el.clientWidth,
  }
}

const POINT_IDLE = 'Not found — point at it on the document'
const POINT_CANCEL = 'Stop pointing'

// The leading colour of a computed box-shadow, "rgb(...) 0px 0px 0px 2px".
const shadowColor = (shadow: string): string => shadow.match(/^(rgba?\([^)]*\))/)?.[1] ?? shadow

// Zoom toolbar and fields grid at one width: EX-01 and EX-02 (D-31).
async function measureReviewChrome(page: Page): Promise<{
  toolbar: Rect
  toolbarFit: { scrollWidth: number; clientWidth: number }
  group: Rect
  segments: Rect[]
  scroll: Rect
  scrollExtent: number
  headerRow: Rect
  heads: number[]
  firstBody: number[]
}> {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((r) => requestAnimationFrame(() => requestAnimationFrame(() => r())))
  })
  return settledRead(
    () =>
      page.evaluate(() => {
        const rect = (el: Element): Rect => {
          const r = el.getBoundingClientRect()
          return { x: r.x, y: r.y, width: r.width, height: r.height }
        }
        const q = (sel: string) => {
          const el = document.querySelector(sel)
          if (!el) throw new Error(`${sel} not found`)
          return el as HTMLElement
        }
        const toolbar = q('[data-testid="extraction-toolbar"]')
        const segments = [...document.querySelectorAll('[data-testid^="extraction-zoom-"]')]
        const group = segments[0]?.parentElement
        const scroll = q('[data-testid="line-item-scroll"]')
        const headerRow = q('[data-testid="line-item-scroll"] thead tr')
        const body = document.querySelector('[data-testid="line-item-scroll"] tbody tr')
        return {
          toolbar: rect(toolbar),
          toolbarFit: { scrollWidth: toolbar.scrollWidth, clientWidth: toolbar.clientWidth },
          group: group ? rect(group) : { x: 0, y: 0, width: 0, height: 0 },
          segments: segments.map(rect),
          scroll: rect(scroll),
          scrollExtent: scroll.scrollWidth,
          headerRow: rect(headerRow),
          heads: [...headerRow.children].map((c) => c.getBoundingClientRect().x),
          firstBody: body ? [...body.children].map((c) => c.getBoundingClientRect().x) : [],
        }
      }),
    'extraction chrome geometry',
  )
}

test.describe('RESKIN2-04 v2 create, progress and review at 1440', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('RK-01 (AC 8): the upload card with a refusal reads its v2 values, and Remove restores the primary', async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = collectErrors(page)

    await startWizard(page, 'RESKIN2-04 upload')
    await expect(page.locator('label[for="pf-import-file"]'), 'dropzone renders').toBeVisible({ timeout: 30_000 })

    // One drop: the PDF sets the run kind, so both CSVs carry a kind note and the 16 MiB one carries the size cap too.
    await dropFiles(page, [
      { name: 'refusal-native.pdf', type: 'application/pdf' },
      { name: 'refusal-oversize.csv', type: 'text/csv', bytes: 16 * 1024 * 1024 },
      { name: 'refusal-wrong-kind.csv', type: 'text/csv' },
    ])

    const acceptedLine = page.getByText(ACCEPTED_LINE, { exact: true })
    await expect(acceptedLine, 'the accepted-types line states every accepted type').toBeVisible()
    const cardBody = acceptedLine.locator('xpath=..')
    const card = cardBody.locator('xpath=..')
    await expect(card, 'the card carries its own header').toContainText('Import invoices ·')
    await expect(cardBody, 'the content div is INSIDE that header, not the card itself').not.toContainText('Import invoices ·')

    const rows = cardBody.locator('ul li')
    await expect(rows, 'three files were dropped, so three rows must be listed').toHaveCount(3)
    const oversize = rows.filter({ hasText: 'refusal-oversize.csv' })
    const wrongKind = rows.filter({ hasText: 'refusal-wrong-kind.csv' })
    const extract = page.getByRole('button', { name: 'Extract invoices' })
    await expect(extract, 'a refusal keeps the primary disabled').toBeDisabled()
    await expect(page.locator('main h1'), 'no h1 inside main on the create view').toHaveCount(0)

    await floor(oversize.locator('p'), 2, 'the oversize row carries the kind note and the size note')
    await floor(wrongKind.locator('p'), 1, 'the wrong-kind row carries the kind note')

    const refused = await readAll(
      page,
      { card, extract, oversize, oversizeNotes: oversize.locator('p'), wrongKind, wrongKindNotes: wrongKind.locator('p') },
      ['--line-1'],
    )
    const cardStyle = refused.reads.card[0].style
    expectCorners(cardStyle, '6px', 'upload card')
    expect(cardStyle['border-top-width'], 'upload card border-top-width').toBe('1px')
    expect(cardStyle['border-top-color'], 'upload card border colour is --line-1').toBe(refused.tokens['--line-1'])
    expect(cardStyle['box-shadow'], 'upload card box-shadow').toBe('none')
    expectCorners(refused.reads.extract[0].style, '7px', 'Extract invoices (refusal)')
    expect(refused.reads.extract[0].style.opacity, 'disabled Extract invoices opacity').toBe('0.45')

    const contrasts: { note: string; ratio: number }[] = []
    for (const { row, notes } of [
      { row: refused.reads.oversize[0], notes: refused.reads.oversizeNotes },
      { row: refused.reads.wrongKind[0], notes: refused.reads.wrongKindNotes },
    ]) {
      for (const note of notes) {
        const ratio = contrastRatio(note.style.color, row.style['background-color'])
        contrasts.push({ note: note.text, ratio })
        expect(ratio, `the note "${note.text}" against its row background`).toBeGreaterThanOrEqual(4.5)
      }
    }
    await attachJson(testInfo, 'rk-01-refusal-reads', { refused, contrasts })
    await shoot(page, testInfo, 'upload-refusal.png')

    for (const name of ['refusal-oversize.csv', 'refusal-wrong-kind.csv']) {
      await rows.filter({ hasText: name }).getByRole('button', { name: 'Remove' }).click()
    }
    await expect(rows, 'only the accepted PDF remains').toHaveCount(1)
    await expect(extract, 'Remove on each refused file enables the primary').toBeEnabled()
    const cleared = await readAll(page, { extract })
    expect(cleared.reads.extract[0].style.opacity, 'enabled Extract invoices opacity').toBe('1')
    expectCorners(cleared.reads.extract[0].style, '7px', 'Extract invoices (enabled)')
    await attachJson(testInfo, 'rk-01-cleared-reads', cleared)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RK-01b (AC 1): with no company the amber panel renders, the primary reads dimmed and nothing is written', async ({ page }, testInfo) => {
    test.setTimeout(120_000)
    const errors = collectErrors(page)

    // The real answer with its entities removed keeps the real CORS headers.
    let stubbed = 0
    await page.route(
      (url) => url.pathname === '/api/portfolio/v1/entities',
      async (route) => {
        if (route.request().method() !== 'GET') return route.continue()
        stubbed += 1
        const response = await route.fetch()
        const body = await response.json()
        await route.fulfill({ response, json: { ...body, entities: [], pagination: { ...body.pagination, total: 0 } } })
      },
    )

    await signInAs(page, 'firm', { tenantId: SHARD.a.id })
    await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
    await expect(page.getByText('Add a client before you file', { exact: true }), 'the no-company panel renders').toBeVisible({ timeout: 30_000 })
    expect(stubbed, 'the entity-list stub answered at least once').toBeGreaterThan(0)

    const recorded = recordWrites(page)
    await page
      .locator('input[type="file"]#pf-import-file')
      .setInputFiles({ name: 'no-company.pdf', mimeType: 'application/pdf', buffer: uniquePdfBytes() })
    const extract = page.getByRole('button', { name: 'Extract invoices' })
    await expect(extract, 'with no company the primary is disabled').toBeDisabled()
    const read = await readAll(page, { extract })
    expect(read.reads.extract[0].style.opacity, 'dimmed Extract invoices opacity').toBe('0.45')
    await attachJson(testInfo, 'rk-01b-reads', read)
    await shoot(page, testInfo, 'upload-no-company.png')

    recorded.stop()
    expect(recorded.writes, `a write left the page:\n${recorded.writes.join('\n')}`).toEqual([])
    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RK-02 (AC 8) + MP-01 + FM-01: the Map step and the manual form read their v2 values and fit at every width', async ({ page }, testInfo) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    await startWizard(page, 'RESKIN2-04 mapping')
    await pickAndRead(page, 'rk-02-unsteered.csv', buildAir07UnsteeredCsv(`INV-RK02-${Date.now()}`))

    const chips = page.locator('main button[draggable="true"]')
    await floor(chips, 1, 'the Map step palette must hold at least one unplaced field chip')
    const paletteCard = chips.first().locator('xpath=../../..')
    await expect(paletteCard, 'the palette chips sit in the Map fields card').toContainText('Map fields to columns')

    const mapRead = await readAll(page, { chips })
    for (const [i, chip] of mapRead.reads.chips.entries()) {
      expectCorners(chip.style, '7px', `palette chip ${i}`)
      expect(chip.style.height, `palette chip ${i} height`).toBe('30px')
    }
    await attachJson(testInfo, 'rk-02-map-reads', mapRead)
    await shoot(page, testInfo, 'mapping.png')

    const mp = await sweep(page, 'MP-01 palette', async (width) => {
      const m = await settledRead(
        async () => ({ card: await paletteCard.boundingBox(), chips: await chips.evaluateAll((els) => els.map((el) => {
          const r = el.getBoundingClientRect()
          return { x: r.x, y: r.y, width: r.width, height: r.height }
        })) }),
        `MP-01 palette at ${width}px`,
      )
      expect(m.card, `the palette card must render at ${width}px`).not.toBeNull()
      expect(m.chips.length, `the palette must still hold chips at ${width}px`).toBeGreaterThan(0)
      for (const [i, chip] of m.chips.entries()) {
        expect(enclosesRect(m.card as Rect, chip, 0.5), `palette chip ${i} lies inside the palette card at ${width}px`).toBe(true)
        for (const other of m.chips.slice(i + 1)) expect(rectsOverlap(chip, other), `two palette chips overlap at ${width}px`).toBe(false)
      }
      return { chips: m.chips.length }
    })
    await attachJson(testInfo, 'rk-02-mp-01', mp)

    await page.getByRole('button', { name: /Back to import/ }).click()
    await page.getByRole('button', { name: 'Skip — enter manually' }).click()
    const descriptionHead = page.locator('main span.label', { hasText: /^Description$/ })
    await expect(descriptionHead, 'the manual form line table renders').toBeVisible({ timeout: 30_000 })
    await page.getByRole('button', { name: /Add line/ }).click()
    const lineHead = descriptionHead.locator('xpath=..')
    const lineTable = lineHead.locator('xpath=..')
    await floor(lineHead.locator('xpath=following-sibling::div'), 2, 'the line table must hold two line rows after Add line')

    const formRead = await readAll(page, { lineTable })
    expectCorners(formRead.reads.lineTable[0].style, '6px', 'line table')
    await attachJson(testInfo, 'rk-02-form-reads', formRead)
    await shoot(page, testInfo, 'manual-form.png')

    const fm = await sweep(page, 'FM-01 line table', async (width) => {
      const m = await settledRead(
        () =>
          lineTable.evaluate((tbl) => {
            const rect = (el: Element) => {
              const r = el.getBoundingClientRect()
              return { x: r.x, y: r.y, width: r.width, height: r.height }
            }
            const [head, ...rows] = [...tbl.children]
            return {
              head: [...head.children].map((c) => c.getBoundingClientRect().left),
              rows: rows.map((row) => ({
                lefts: [...row.children].map((c) => c.getBoundingClientRect().left),
                box: rect(row),
                remove: rect(row.querySelector('button[aria-label^="Remove line"]') as Element),
                scrollWidth: row.scrollWidth,
                clientWidth: row.clientWidth,
              })),
            }
          }),
        `FM-01 line table at ${width}px`,
      )
      expect(m.rows.length, `the line table must hold at least two rows at ${width}px`).toBeGreaterThanOrEqual(2)
      for (const [i, row] of m.rows.entries()) {
        expect(row.lefts.length, `row ${i} must carry the header's cell count at ${width}px`).toBe(m.head.length)
        for (const [c, left] of row.lefts.entries()) {
          expect(Math.abs(left - m.head[c]), `row ${i} cell ${c} left edge vs the header at ${width}px`).toBeLessThanOrEqual(1)
        }
        expect(enclosesRect(row.box, row.remove, 0.5), `row ${i} remove button lies inside its row at ${width}px`).toBe(true)
        expect(row.scrollWidth, `row ${i} must not overflow sideways at ${width}px`).toBeLessThanOrEqual(row.clientWidth + 1)
      }
      return { rows: m.rows.length }
    })
    await attachJson(testInfo, 'rk-02-fm-01', fm)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RK-03 (AC 8) + PG-01: the progress card reads its v2 values and fits at every width, then the run lands', async ({ page }, testInfo) => {
    test.setTimeout(420_000)
    const errors = collectErrors(page)

    // Held at the extraction poll, not the upload: a row shows its bar only once a stage is reported, and an upload
    // that never completes reports none (the row reads QUEUED).
    let held = true
    let heldPolls = 0
    await page.route('**/api/submission/v1/extractions*', async (route) => {
      const documentId = new URL(route.request().url()).searchParams.get('document_id')
      if (!held || route.request().method() !== 'GET' || documentId === null) return route.continue()
      heldPolls += 1
      const response = await route.fetch()
      await route.fulfill({
        response,
        status: 200,
        json: { jobs: [{ id: 'e2e-hold', document_id: documentId, state: 'extracting', created_at: new Date().toISOString(), last_error: null, failure_kind: null }] },
      })
    })

    await startWizard(page, 'RESKIN2-04 progress')
    await page
      .locator('input[type="file"]#pf-import-file')
      .setInputFiles({ name: 'progress-card.pdf', mimeType: 'application/pdf', buffer: uniquePdfBytes() })
    await page.getByRole('button', { name: 'Extract invoices' }).click()

    const card = page.getByTestId('import-progress')
    await expect(card, 'the progress card must mount').toBeVisible({ timeout: 30_000 })
    await expect(card.getByText('READING', { exact: true }), 'the held poll puts the row in flight').toBeVisible({ timeout: 60_000 })
    expect(heldPolls, 'the poll stub answered at least once').toBeGreaterThan(0)
    const column = card.locator('xpath=..')
    const bar = card.locator('[data-testid="import-progress-row"] > span:nth-child(2) > span:first-child')
    await floor(bar, 1, 'at least one in-flight row must carry a bar')

    const read = await readAll(page, { card, column, bar })
    const cardBox = read.reads.card[0].rect
    const columnBox = read.reads.column[0].rect
    expect(enclosesRect(columnBox, cardBox, 0.5), 'import-progress lies inside its column').toBe(true)
    expect(cardBox.width, 'import-progress is no wider than 520px').toBeLessThanOrEqual(520)
    for (const [i, b] of read.reads.bar.entries()) {
      expectCorners(b.style, '3px', `progress bar ${i}`)
      expect(b.style['background-image'], `progress bar ${i} background-image`).toContain('repeating-linear-gradient')
      expect(b.style['animation-duration'], `progress bar ${i} animation-duration`).toBe('2.3s')
    }
    await attachJson(testInfo, 'rk-03-reads', read)
    await shoot(page, testInfo, 'progress-card.png')

    const pg = await sweep(page, 'PG-01 progress card', async (width) => {
      const m = await settledRead(
        () =>
          card.evaluate((c) => {
            const rect = (el: Element) => {
              const r = el.getBoundingClientRect()
              return { x: r.x, y: r.y, width: r.width, height: r.height }
            }
            const col = c.parentElement as HTMLElement
            const cr = col.getBoundingClientRect()
            const cs = getComputedStyle(col)
            const e = {
              left: cr.left + parseFloat(cs.borderLeftWidth) + parseFloat(cs.paddingLeft),
              right: cr.right - parseFloat(cs.borderRightWidth) - parseFloat(cs.paddingRight),
            }
            const bars = [...c.querySelectorAll('[data-testid="import-progress-row"]')].flatMap((row) => {
              const group = row.children[1]
              const barEl = group?.children[0]
              const label = group?.children[1]
              if (!barEl || !label || !getComputedStyle(barEl).backgroundImage.includes('repeating-linear-gradient')) return []
              return [{ bar: rect(barEl), label: rect(label) }]
            })
            return { card: rect(c), content: { left: e.left, right: e.right }, bars }
          }),
        `PG-01 progress card at ${width}px`,
      )
      const centre = m.card.x + m.card.width / 2
      expect(m.card.width, `the card is no wider than its column's content at ${width}px`).toBeLessThanOrEqual(m.content.right - m.content.left + 0.5)
      expect(Math.abs(centre - (m.content.left + m.content.right) / 2), `the card is centred in its column at ${width}px`).toBeLessThanOrEqual(1)
      expect(m.bars.length, `an in-flight bar must be measurable at ${width}px`).toBeGreaterThan(0)
      for (const [i, row] of m.bars.entries()) {
        expect(row.bar.x + row.bar.width, `bar ${i} lies left of its stage label at ${width}px`).toBeLessThanOrEqual(row.label.x + 0.5)
        expect(rectsOverlap(row.bar, row.label), `bar ${i} overlaps its stage label at ${width}px`).toBe(false)
        expect(
          Math.abs(row.bar.y + row.bar.height / 2 - (row.label.y + row.label.height / 2)),
          `bar ${i} shares its label's vertical centre at ${width}px`,
        ).toBeLessThanOrEqual(1)
      }
      return { cardWidth: m.card.width, bars: m.bars.length }
    })
    await attachJson(testInfo, 'rk-03-pg-01', pg)

    held = false
    await expect(page.getByTestId('extraction-review'), 'the released run lands on its extraction review').toBeVisible({ timeout: 240_000 })

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RK-04 (AC 8) + RV-01 + RV-02 + RV-03: the mixed import reads its v2 review values and fits at every width', async ({ page }, testInfo) => {
    test.setTimeout(300_000)
    const errors = collectErrors(page)

    const { token, entityId } = await startWizard(page, 'RESKIN2-04 review')
    await pickAndRead(page, 'rk-04-mixed.csv', buildMixedCsv())
    await placeMixedColumns(page)
    await clickImport(page)

    const heading = page.getByRole('heading', { level: 2, name: '2 invoices imported' })
    await expect(heading, 'the mixed import lands on its review').toBeVisible({ timeout: 60_000 })
    // The validated rows arm approval runs at import; closing them makes the rows selectable for the bulk bar.
    await expect
      .poll(async () => (await listInvoices(token, { entity_id: entityId })).invoices.filter((inv) => inv.approval?.run_state === 'open').length, {
        message: 'the import must arm at least one open approval run before the runs are closed',
        timeout: 60_000,
      })
      .toBeGreaterThan(0)
    await approveOpenRunsForEntity(token, entityId)
    await page.reload()
    await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
    await expect(heading, 'the review survives the reload').toBeVisible({ timeout: 60_000 })

    const reviewRows = page.getByTestId('review-row')
    await floor(reviewRows, 2, 'the invoices tab must list both imported invoices')
    const violateRow = reviewRows.filter({ hasText: 'INV-UI-MIX-VIOLATE' })
    const cleanRow = reviewRows.filter({ hasText: 'INV-UI-MIX-CLEAN' })
    await expect(violateRow).toBeVisible()
    await expect(cleanRow).toBeVisible()
    await expect(page.locator('main h1'), 'no h1 inside main on the review').toHaveCount(0)

    const invoicesTab = page.getByRole('button', { name: /^Invoices \(\d+\)$/ })
    const filterPills = page.getByTestId('review-filter-pill')
    const railPills = page.getByTestId('review-rail-pill')
    const verdictPills = page.getByTestId('review-verdict').locator('xpath=./*[1]')
    const tileFigure = page.getByText('1 valid', { exact: true })
    await floor(filterPills, 1, 'the toolbar must hold at least one filter pill')
    await floor(railPills, 1, 'the failing-rules rail must hold a pill for the failing invoice')
    await floor(verdictPills, 1, 'the table must hold at least one verdict pill')

    const review = await readAll(
      page,
      { heading, tileFigure, invoicesTab, filterPills, railPills, verdictPills },
      ['--action'],
    )
    const h2 = review.reads.heading[0].style
    expect(firstFamily(h2['font-family']), 'review h2 first font family').toBe('Manrope')
    expect(h2['font-weight'], 'review h2 font weight').toBe('700')
    expect(firstFamily(review.reads.tileFigure[0].style['font-family']), 'review tile figure first font family').toBe('Manrope')
    const tab = review.reads.invoicesTab[0].style
    expect(tab['border-bottom-width'], 'active tab border-bottom-width').toBe('2px')
    expect(tab['border-bottom-style'], 'active tab border-bottom-style').toBe('solid')
    expect(tab['border-bottom-color'], 'active tab underline is the resolved --action').toBe(review.tokens['--action'])
    expectCorners(review.reads.filterPills[0].style, '4px', 'filter pill')
    expectCorners(review.reads.railPills[0].style, '4px', 'rail pill')
    expectCorners(review.reads.verdictPills[0].style, '4px', 'verdict pill')
    await attachJson(testInfo, 'rk-04-review-reads', review)

    const table = page.getByTestId('review-table')
    const rv1 = await sweep(page, 'RV-01 review table', async (width) => {
      const m = await settledRead(
        () =>
          table.evaluate((t) => {
            const rect = (el: Element) => {
              const r = el.getBoundingClientRect()
              return { x: r.x, y: r.y, width: r.width, height: r.height }
            }
            const head = t.querySelector('.pf-list-head') as HTMLElement
            const row = t.querySelector('[data-testid="review-row"]') as HTMLElement
            return {
              head: [...head.children].map((c) => c.getBoundingClientRect().left),
              row: [...row.children].map(rect),
              rowBox: rect(row),
            }
          }),
        `RV-01 review table at ${width}px`,
      )
      expect(m.row.length, `the row must carry the header's cell count at ${width}px`).toBe(m.head.length)
      for (const [c, cell] of m.row.entries()) {
        expect(Math.abs(cell.x - m.head[c]), `column ${c} header left edge vs its first data cell at ${width}px`).toBeLessThanOrEqual(1)
      }
      const chevron = m.row[m.row.length - 1]
      expect(enclosesRect(m.rowBox, chevron, 0.5), `the chevron lies inside the row at ${width}px`).toBe(true)
      for (const other of m.row.slice(0, -1)) {
        expect(chevron.x + chevron.width, `the chevron is the rightmost cell at ${width}px`).toBeGreaterThanOrEqual(other.x + other.width - 0.5)
      }
      await assertPageDoesNotScrollSideways(page, `RV-01 at ${width}px`)
      return { cells: m.row.length }
    })
    await attachJson(testInfo, 'rk-04-rv-01', rv1)

    // The bulk bar: select the clean row, read the bar idle and armed, then cancel. Confirm is never clicked.
    await cleanRow.getByTestId('review-select').click()
    const bulkBar = page.getByTestId('review-bulk-bar')
    await expect(bulkBar, 'selecting a validated row shows the bulk bar').toBeVisible()
    const idle = await readAll(page, { bulkBar }, ['--line-2'])
    expect(idle.reads.bulkBar[0].style['border-top-color'], 'idle bulk bar border is the resolved --line-2').toBe(idle.tokens['--line-2'])
    const recorded = recordWrites(page)
    await page.getByTestId('review-bulk-submit').click()
    await expect(page.getByTestId('review-bulk-confirm'), 'arming shows the confirm step').toBeVisible()
    const armed = await readAll(page, { bulkBar }, ['--line-2'])
    expect(armed.reads.bulkBar[0].style['border-top-color'], 'armed bulk bar border is the resolved --line-2').toBe(armed.tokens['--line-2'])
    await page.getByTestId('review-bulk-cancel').click()
    await expect(page.getByTestId('review-bulk-confirm'), 'cancel leaves the confirm step').toHaveCount(0)
    recorded.stop()
    expect(recorded.writes, `arm then cancel wrote:\n${recorded.writes.join('\n')}`).toEqual([])
    await attachJson(testInfo, 'rk-04-bulk-bar', { idle, armed })
    await shoot(page, testInfo, 'review-invoices.png')

    // RV-02: the failing row's expansion.
    await violateRow.click()
    const expansion = page.getByTestId('review-row-expansion')
    const fixCards = expansion.getByTestId('review-fix-card')
    await floor(fixCards, 1, 'the failing row must open at least one fix card')
    const fixCard = fixCards.filter({ has: page.getByTestId('review-fix-input') }).first()
    await expect(fixCard, 'a fix card with an input must render').toBeVisible()
    const fixInput = fixCard.getByTestId('review-fix-input')
    const keepReason = expansion.getByTestId('review-keep-reason')
    const keep = expansion.getByTestId('review-keep')
    await expect(keepReason).toBeVisible()
    const invoiceCell = violateRow.locator('xpath=./*[2]')
    const rv2 = await sweep(page, 'RV-02 expansion', async (width) => {
      const m = await settledRead(
        async () => ({
          firstCard: await fixCards.first().boundingBox(),
          cell: await invoiceCell.boundingBox(),
          card: await fixCard.boundingBox(),
          input: await fixInput.boundingBox(),
          expansion: await expansion.boundingBox(),
          keepReason: await keepReason.boundingBox(),
          keep: await keep.boundingBox(),
        }),
        `RV-02 expansion at ${width}px`,
      )
      for (const [name, box] of Object.entries(m)) expect(box, `${name} must render at ${width}px`).not.toBeNull()
      const r = m as Record<keyof typeof m, Rect>
      expect(Math.abs(r.firstCard.x - r.cell.x), `the first fix card's left edge vs the invoice-number cell at ${width}px`).toBeLessThanOrEqual(1)
      expect(enclosesRect(r.card, r.input, 0.5), `the fix input lies inside its card at ${width}px`).toBe(true)
      expect(enclosesRect(r.expansion, r.keepReason, 0.5), `the keep reason input lies inside the expansion at ${width}px`).toBe(true)
      expect(enclosesRect(r.expansion, r.keep, 0.5), `Keep as-is lies inside the expansion at ${width}px`).toBe(true)
      expect(rectsOverlap(r.keepReason, r.keep), `the keep reason input and Keep as-is overlap at ${width}px`).toBe(false)
      return { cardLeft: r.firstCard.x, cellLeft: r.cell.x }
    })
    await attachJson(testInfo, 'rk-04-rv-02', rv2)
    await violateRow.click()

    // RV-03 on the unreadable tab.
    await page.getByRole('button', { name: /^Unreadable rows \(\d+\)$/ }).click()
    await expect(page.getByText('rows disagree on issue_date'), 'the unreadable tab lists the structural rows').toHaveCount(2)
    const rv3 = await sweep(page, 'RV-03 unreadable tab', (width) => assertGridAligned(page, 'Why it could not be read', `RV-03 unreadable at ${width}px`))
    await attachJson(testInfo, 'rk-04-rv-03-unreadable', rv3)
    await shoot(page, testInfo, 'review-unreadable.png')

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RK-05 (AC 6) + MP-01 + RV-03: the second import of the same text shows the already-imported tab', async ({ page }, testInfo) => {
    test.setTimeout(300_000)
    const errors = collectErrors(page)

    await startWizard(page, 'RESKIN2-04 already imported')
    // Run 1 stores the mixed invoices and saves its mapping.
    await pickAndRead(page, 'rk-05-first.csv', buildMixedCsv())
    await placeMixedColumns(page)
    await clickImport(page)
    await expect(page.getByRole('button', { name: /^Invoices \(\d+\)$/ })).toBeVisible({ timeout: 60_000 })
    await page.getByRole('button', { name: 'Finish · go to invoices' }).click()

    // Run 2: the same text opens with the restored mapping, and every invoice collides.
    await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
    await pickAndRead(page, 'rk-05-second.csv', buildMixedCsv())
    const notice = page.getByTestId('map-restored-notice')
    await expect(notice, 'the second import of the same text opens with a restored mapping').toBeVisible({ timeout: 30_000 })
    const noticeButton = notice.getByRole('button', { name: 'Use automatic suggestions' })
    const noticeText = notice.locator('p')
    const mp = await sweep(page, 'MP-01 restored notice', async (width) => {
      const m = await settledRead(
        async () => ({ notice: await notice.boundingBox(), button: await noticeButton.boundingBox(), text: await noticeText.boundingBox() }),
        `MP-01 restored notice at ${width}px`,
      )
      for (const [name, box] of Object.entries(m)) expect(box, `the notice ${name} must render at ${width}px`).not.toBeNull()
      const r = m as Record<keyof typeof m, Rect>
      expect(enclosesRect(r.notice, r.button, 0.5), `the notice button lies inside the notice box at ${width}px`).toBe(true)
      expect(rectsOverlap(r.button, r.text), `the notice button overlaps its text at ${width}px`).toBe(false)
      return { noticeWidth: r.notice.width }
    })
    await attachJson(testInfo, 'rk-05-mp-01', mp)

    await clickImport(page)
    await expect(page.getByRole('button', { name: /^Invoices \(\d+\)$/ })).toBeVisible({ timeout: 60_000 })
    await page.getByRole('button', { name: /^Already imported \(\d+\)$/ }).click()
    await expect(page.getByTestId('review-already-imported-tab'), 'the already-imported tab opens').toBeVisible()
    const rv3 = await sweep(page, 'RV-03 already-imported tab', (width) =>
      assertGridAligned(page, 'Invoice already in your ledger', `RV-03 already imported at ${width}px`),
    )
    await attachJson(testInfo, 'rk-05-rv-03-already-imported', rv3)
    await shoot(page, testInfo, 'review-already-imported.png')

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RK-06 (AC 6) + RV-04: the all-rejected run keeps its tiles inside the card', async ({ page }, testInfo) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    await startWizard(page, 'RESKIN2-04 rejected')
    await pickAndRead(page, 'rk-06-header-only.csv', buildHeaderOnlyCsv())
    await page.getByRole('button', { name: 'invoice_number' }).click()
    await page.getByText('Invoice No', { exact: true }).click()
    await expect(page.getByRole('button', { name: 'Import 0 rows' })).toBeEnabled()
    await clickImport(page)

    const title = page.getByText('Nothing was imported', { exact: true })
    await expect(title, "the rejected run's card renders").toBeVisible({ timeout: 30_000 })
    const rejectedCard = title.locator('xpath=..')
    const tiles = rejectedCard
      .locator(':scope > div')
      .filter({ has: page.getByText('Invoices created', { exact: true }) })
      .locator(':scope > div')
    await expect(tiles, 'the rejected card holds its three tiles').toHaveCount(3)
    await shoot(page, testInfo, 'review-rejected-run.png')

    const rv4 = await sweep(page, 'RV-04 rejected tiles', async (width) => {
      const m = await settledRead(
        async () => ({
          card: await rejectedCard.boundingBox(),
          tiles: await tiles.evaluateAll((els) => els.map((el) => {
            const r = el.getBoundingClientRect()
            return { x: r.x, y: r.y, width: r.width, height: r.height }
          })),
        }),
        `RV-04 rejected tiles at ${width}px`,
      )
      expect(m.card, `the rejected card must render at ${width}px`).not.toBeNull()
      expect(m.tiles.length, `the rejected card must still hold its tiles at ${width}px`).toBe(3)
      for (const [i, tile] of m.tiles.entries()) {
        expect(enclosesRect(m.card as Rect, tile, 0.5), `tile ${i} lies inside the rejected card at ${width}px`).toBe(true)
        expect(Math.abs(tile.y - m.tiles[0].y), `tile ${i} shares tile 0's top at ${width}px`).toBeLessThanOrEqual(1)
        for (const other of m.tiles.slice(i + 1)) expect(rectsOverlap(tile, other), `two tiles overlap at ${width}px`).toBe(false)
      }
      return { tiles: m.tiles.length }
    })
    await attachJson(testInfo, 'rk-06-rv-04', rv4)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })
})

test.describe('RESKIN2-04 v2 extraction review at 1440', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('RX-01 (AC 8) + RX-03 (AC 5) + EX-01 + EX-02: the flagged reading reads its v2 values, selecting, and fits at every width', async ({ page }, testInfo) => {
    test.setTimeout(420_000)
    const errors = collectErrors(page)

    await extractOneDocument(page, 'RESKIN2-04 extraction flagged')
    const detail = await openExtractionReview(page)
    await expect(page.locator('main h1'), 'no h1 inside main on the extraction view').toHaveCount(0)

    const flagged = detail.fields.filter((f) => f.reason !== '')
    expect(flagged.length, 'the default upload must read with flagged fields').toBeGreaterThan(0)
    await attachJson(testInfo, 'rx-01-flag-count', { flagged: flagged.length, reasons: flagged.map((f) => `${f.name}:${f.reason}`) })

    const input = page.locator('input[data-testid^="extraction-input-"]:not([readonly])').first()
    await expect(input, 'a header field must render an editable input').toBeVisible()
    await input.focus()
    const focused = await readAll(page, { input }, ['--ring'])
    expect(focused.tokens['--ring'], '--ring resolves to the design teal').toBe('rgb(56, 135, 126)')
    expect(focused.reads.input[0].style['border-top-color'], 'focused field border colour is the resolved --ring').toBe(focused.tokens['--ring'])
    expect(shadowColor(focused.reads.input[0].style['box-shadow']), 'focused field box-shadow colour is the resolved --ring').toBe(focused.tokens['--ring'])
    await input.blur()

    const pillCell = page.locator('[data-testid^="extraction-field-"]').filter({ has: page.locator('span.mono') }).first()
    const pills = pillCell.locator('span.mono')
    const chips = page.locator('button[data-testid^="extraction-chip-"]')
    const zoomSegments = page.locator('[data-testid^="extraction-zoom-"]')
    const zoomGroup = zoomSegments.first().locator('xpath=..')
    const frame = page.getByTestId('extraction-page-1')
    const lineScroll = page.getByTestId('line-item-scroll')
    await floor(pills, 1, 'a flagged header field must render a reason pill')
    await floor(chips, 1, 'the ambiguous field must render at least one chip')
    await floor(zoomSegments, 1, 'the zoom group must render its segments')
    await floor(lineScroll, 1, 'the default upload must read with line items')

    const read = await readAll(page, { pill: pills.first(), chip: chips, group: zoomGroup, segments: zoomSegments, frame, lineScroll })
    expectCorners(read.reads.pill[0].style, '4px', 'reason pill')
    for (const [i, chip] of read.reads.chip.entries()) expectCorners(chip.style, '6px', `chip ${i}`)
    expectCorners(read.reads.group[0].style, '6px', 'zoom group')
    expect(read.reads.segments.length, 'the zoom group holds at least one segment').toBeGreaterThan(0)
    for (const [i, seg] of read.reads.segments.entries()) expectCorners(seg.style, '4px', `zoom segment ${i}`)
    expect(read.reads.frame[0].style['box-shadow'], 'page frame box-shadow').toBe('none')
    expectCorners(read.reads.lineScroll[0].style, '6px', 'line-item-scroll')
    expect(read.reads.lineScroll[0].style['border-top-width'], 'line-item-scroll border').toBe('1px')
    await attachJson(testInfo, 'rx-01-reads', read)
    await assertPageDoesNotScrollSideways(page, 'extraction review at 1440')
    expect((await boxOf(page, 'extraction-page-1')).width, 'the page frame has a width').toBeGreaterThan(0)
    await shootExtraction(page, testInfo, 'extraction-flagged.png')

    // RX-03: point at a missing field, read the armed button, then stop.
    // A header field only: the point button is not rendered for a line-item field.
    const missingNames = detail.fields.filter((f) => f.reason === 'missing').map((f) => f.name)
    let missing: { name: string } | undefined
    for (const name of missingNames) {
      if ((await page.getByTestId(`extraction-point-${name}`).count()) > 0) {
        missing = { name }
        break
      }
    }
    expect(missing, `no header field is missing -- there is nothing to point at (missing: ${missingNames.join(', ')})`).toBeTruthy()
    const point = page.getByTestId(`extraction-point-${missing!.name}`)
    await expect(point, `${missing!.name} offers no way to point at it`).toBeVisible({ timeout: 30_000 })
    await point.click()
    const stop = page.getByTestId(`extraction-point-cancel-${missing!.name}`)
    await expect(stop, 'arming shows Stop pointing').toBeVisible({ timeout: 15_000 })
    await expect(stop, 'the Stop pointing label').toHaveText(POINT_CANCEL)
    const armed = await readAll(page, { point })
    expectCorners(armed.reads.point[0].style, '6px', 'armed point button')
    await attachJson(testInfo, 'rx-03-reads', armed)
    await shootExtraction(page, testInfo, 'extraction-selecting.png')
    await stop.click()
    await expect(stop, 'Stop pointing leaves the armed state').toHaveCount(0)
    await expect(point, 'the field returns to idle').toHaveText(POINT_IDLE)

    // EX-01 and EX-02 over every width.
    const swept = await sweep(page, 'EX-01 + EX-02', async () => measureReviewChrome(page))
    for (const m of swept) {
      expect(m.segments.length, `zoom segments at ${m.width}px`).toBeGreaterThan(0)
      expect(m.toolbarFit.scrollWidth, `the toolbar spills sideways at ${m.width}px`).toBeLessThanOrEqual(m.toolbarFit.clientWidth)
      expect(enclosesRect(m.toolbar, m.group), `the zoom group lies inside the toolbar at ${m.width}px`).toBe(true)
      for (const [i, seg] of m.segments.entries()) {
        expect(enclosesRect(m.group, seg), `zoom segment ${i} lies inside the group at ${m.width}px`).toBe(true)
        for (const other of m.segments.slice(i + 1)) {
          expect(rectsOverlap(seg, other), `two zoom segments overlap at ${m.width}px`).toBe(false)
        }
      }
      expect(m.heads.length, `grid header cells at ${m.width}px`).toBeGreaterThan(0)
      expect(m.firstBody.length, `grid first body row cells at ${m.width}px`).toBe(m.heads.length)
      // At 1280 the table overflows its box by design (EXTR13-LAYOUT-04), so the row is held to the scroll extent.
      const extent: Rect = { ...m.scroll, width: Math.max(m.scroll.width, m.scrollExtent + 2) }
      expect(enclosesRect(extent, m.headerRow, 1), `the header row lies inside the scroll extent at ${m.width}px`).toBe(true)
      for (const [c, x] of m.heads.entries()) {
        expect(Math.abs(x - m.firstBody[c]), `header cell ${c} left edge vs its first body cell at ${m.width}px`).toBeLessThanOrEqual(1)
      }
    }
    await attachJson(testInfo, 'ex-01-ex-02-sweep', swept)
    const fit = await lineScroll.evaluate(edgesOf)
    await attachJson(testInfo, 'ex-02-scroll-edges', fit)

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RX-02 (AC 8): the confident reading renders no reason pill', async ({ page }, testInfo) => {
    test.setTimeout(420_000)
    const errors = collectErrors(page)

    await extractOneDocument(page, 'RESKIN2-04 extraction confident')
    // Read-side only: the detail GET is answered with every reason cleared; values, regions and lines stay.
    const DETAIL_GLOB = /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/
    const cleared: { fields: number; reasonsBefore: number } = { fields: 0, reasonsBefore: 0 }
    await page.route(DETAIL_GLOB, async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      const response = await route.fetch()
      const body = (await response.json()) as ExtractionDetail
      cleared.fields = body.fields.length
      cleared.reasonsBefore = body.fields.filter((f) => f.reason !== '').length
      await route.fulfill({ response, json: { ...body, fields: body.fields.map((f) => ({ ...f, reason: '' })) } })
    })
    try {
      const detail = await openExtractionReview(page)
      expect(detail.fields.length, 'the transformed reading must carry fields').toBeGreaterThan(0)
      expect(detail.fields.filter((f) => f.reason !== '').length, 'every reason is cleared on the wire').toBe(0)

      const cells = page.locator('[data-testid^="extraction-field-"]')
      await floor(cells, 1, 'the review must render its header fields')
      // A header cell's pill slot also carries the NO REGION cue; only a reason pill counts.
      const reasonPills = await cells.evaluateAll(
        (els) => els.filter((el) => { const pill = el.querySelector('span.mono'); return pill !== null && pill.textContent !== 'NO REGION' }).length,
      )
      expect(reasonPills, 'a confident reading draws no reason pill in a header cell').toBe(0)
      await attachJson(testInfo, 'rx-02-reason-counts', { renderedCells: await cells.count(), reasonPills, ...cleared })
      await shootExtraction(page, testInfo, 'extraction-confident.png')
    } finally {
      await page.unroute(DETAIL_GLOB)
    }

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })

  test('RX-04: a rule break reads like its inconsistent sibling on the deployed build', async ({ page }) => {
    test.setTimeout(420_000)
    const errors = collectErrors(page)

    await extractOneDocument(page, 'ENGI-18 rule break')
    // Read-side only: total becomes a rule break (extraction.ReasonRuleBreak); subtotal stays inconsistent.
    const MESSAGE = Array.from({ length: 50 }, (_, i) => `rule${i}`).join(' ').slice(0, 300).trimEnd()
    const DETAIL_GLOB = /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/
    await page.route(DETAIL_GLOB, async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      const response = await route.fetch()
      const body = (await response.json()) as ExtractionDetail
      const fields = body.fields.map((f) =>
        f.name === 'total' ? { ...f, reason: 'rule_break', rules: [{ key: 'total_sum', message: MESSAGE }] } : f,
      )
      await route.fulfill({ response, json: { ...body, fields } })
    })
    try {
      await openExtractionReview(page)

      const cell = page.getByTestId('extraction-field-total')
      const pill = cell.getByText('BREAKS A RULE', { exact: true })
      const sibling = page.getByTestId('extraction-field-subtotal').getByText("DOESN'T ADD UP", { exact: true })
      await expect(pill, 'the rule break must draw its pill').toBeVisible({ timeout: 60_000 })
      await expect(sibling, 'the inconsistent sibling must keep its pill').toBeVisible()

      const look = (loc: Locator) =>
        loc.evaluate((el) => {
          const cs = getComputedStyle(el)
          return { color: cs.color, background: cs.backgroundColor, border: cs.borderTopColor, fontSize: cs.fontSize }
        })
      expect(await look(pill), 'the rule-break pill is styled apart from its inconsistent sibling').toEqual(await look(sibling))

      const note = cell.getByText(MESSAGE, { exact: true })
      await expect(note, 'the note must carry the whole rule message').toBeVisible()
      const noteBox = (await note.boundingBox())!
      const cellBox = (await cell.boundingBox())!
      expect(noteBox.x + noteBox.width, 'the note spills out of its cell').toBeLessThanOrEqual(cellBox.x + cellBox.width + 0.5)
      const scroll = await cell.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
      expect(scroll.scrollWidth, 'the cell scrolls sideways').toBeLessThanOrEqual(scroll.clientWidth)

      expect(await page.getByTestId('extraction-fields').innerText(), 'the raw code reached the screen').not.toContain('rule_break')
    } finally {
      await page.unroute(DETAIL_GLOB)
    }

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })
})

test.describe('ENGI-16 import review opens the editor at the line', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('ENGI-16 review: Open line 2 lands on the line in the editor', async ({ page }) => {
    test.setTimeout(180_000)
    const errors = collectErrors(page)

    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: `ENGI-16 review ${Date.now()}`, tin: freshTin() })
    const invoiceNumber = `INV-ENGI16-${Date.now()}`
    const batchId = await createImportBatch(token, entity.id, invoiceNumber, [
      { item: 'Widget A', qty: '1', unitPrice: '100.00' },
      { item: 'Widget B', qty: '1', unitPrice: '-5.00' },
    ])

    await signInAs(page, 'firm', { tenantId: SHARD.a.id, path: `/imports/${batchId}/review` })
    const row = page.getByTestId('review-row').filter({ hasText: invoiceNumber })
    await expect(row).toBeVisible({ timeout: 60_000 })
    await row.click()

    const open = page.getByTestId('review-fix-open-line').filter({ hasText: 'Open line 2' })
    await expect(open).toBeVisible()
    const card = page.getByTestId('review-fix-card').filter({ has: open })

    const entry = page.viewportSize()
    try {
      for (const width of WIDE_WIDTHS) {
        await page.setViewportSize({ width, height: 900 })
        await settleAnimations(card, open)
        await expect
          .poll(async () => enclosesRect((await card.boundingBox())!, (await open.boundingBox())!, 1), {
            message: `review-fix-open-line sits inside its review-fix-card at ${width}px`,
          })
          .toBe(true)
        await assertPageDoesNotScrollSideways(page, `review at ${width}px`)
      }
    } finally {
      if (entry) await page.setViewportSize(entry)
    }

    await open.click()
    await expect(page, 'Open line 2 leaves the review for the invoice').toHaveURL(/\/invoices\/[0-9a-f-]{36}$/)
    await expect(page.getByTestId('edit-invoice')).toBeVisible()
    // line-cost-non-negative reports the bare path line_items[2], so focus falls to the row's first input.
    const first = page.getByTestId('line-row').nth(1).locator('[data-line-field="description"]')
    await expect(first, 'line-row 2 first input takes focus').toBeFocused()
    await expect(first, 'line-row 2 first input is in the viewport').toBeInViewport()

    expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
  })
})
