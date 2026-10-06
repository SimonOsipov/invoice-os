// Helpers shared by the import-wizard specs. Declares no test and no hook.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { basename, dirname, join } from 'node:path'

import { test, expect, type Page, type Request } from '@playwright/test'
import { login, createEntity, type ExtractionDetail, type ExtractionReason, type Persona } from '../api/client'
import { freshTin } from '../api/fixtures'
import { signInAs } from '../personaSession'
import { approvalRun404Dropper, type Dropper } from './consoleGate'
import type { Rect } from './layout'
import { shardTenants } from './targets'

// The tenants of the shard that runs specFile; PERSONAS shadows the api client's 1111 / 2222 pair.
export function shardPersonas(specFile: string): { SHARD: ReturnType<typeof shardTenants>; PERSONAS: { A: Persona; B: Persona } } {
  const SHARD = shardTenants(specFile)
  const PERSONAS: { A: Persona; B: Persona } = {
    A: { ...SHARD.a, tenantId: SHARD.a.id },
    B: { ...SHARD.b, tenantId: SHARD.b.id },
  }
  return { SHARD, PERSONAS }
}

// collectErrors(): console/pageerror collection, with the approval-run 404 dropped.
export function collectErrors(page: Page, extra?: Dropper): string[] {
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

// selectEntity(): a second copy of invoice-surfaces.spec.ts's own helper of the same
// name (this package's established convention for small Page-driving helpers).
// Needed here by the persona-handoff-fix regression fix ([entity-id-restored]):
// Invoices is a CLIENT-scoped surface now (listInvoices' own `entity_id` param,
// server-side), so a test that drives it must make ITS OWN fixture entity the active
// workspace switcher selection first -- signInAs() alone leaves the switcher on
// whatever `clients[0]` resolves to (portfolio's List `ORDER BY name ASC, id ASC`),
// never this test's own Date.now()-suffixed entity. Sidebar.tsx:
// data-testid="company-switcher" (the toggle button) / "company-switcher-option"
// (each row in the open dropdown).
export async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// requestBody(): the multipart body Chromium recorded, for the specs that
// inspect a request rather than a response. postDataBuffer first (postData() decodes
// as text and is null for some bodies); '' when nothing was recorded, so an assertion
// over it fails loudly instead of passing vacuously.
export function requestBody(req: Request): string {
  return req.postDataBuffer()?.toString('utf8') ?? req.postData() ?? ''
}

export const DOCUMENT_FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents')
// Committed by EXTR-09-03, re-pointed at the rich fixture by EXTR-18-05 so the wire-derived
// specs read real field content instead of the mock's fixed shape.
const NATIVE_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'rich_invoice.pdf'))

// Fresh bytes per pick, contract-document-upload.spec.ts's own recipe: a trailing PDF
// comment moves the content hash without moving a byte offset, so `startxref` still
// resolves. Without it, per-tenant dedupe reuses an earlier row and the PERMANENT
// per-document enqueue key skips the enqueue — the poll would then settle on a PREVIOUS
// run's job and stay green while extraction is broken.
export function uniquePdfBytes(): Buffer {
  return Buffer.concat([NATIVE_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// A file named *.pdf whose bytes are NOT a PDF: classification is extension-only and
// upload only hashes+PUTs bytes (classify.go / service.go), so this sails through
// selection and upload, then fails pdfium.OpenDocument on every one of River's 3
// attempts (worker.go) and dead-letters deterministically. Fresh bytes per call, same
// permanent-enqueue-key reason as uniquePdfBytes().
export function uniqueGarbageBytes(): Buffer {
  return Buffer.concat([Buffer.from('not a pdf at all'), Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// The CONTENT box, never the bounding box: children sit inside the card's own gutter, so
// a border-box comparison passes a row that overflows it. scrollWidth/clientWidth ride
// along because a stretched flex item keeps its box while its TEXT overflows — and this
// story grew the accepted-types line from three tokens to eight.
export function edgesOf(el: HTMLElement) {
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

// Two consecutive AGREEING reads, never one: a boundingBox taken as a panel opens measures
// the transform mid-flight, and two different values on two reads is the tell.
// Returns the agreed read itself: a fresh read after the poll can land on a later relayout.
export async function settledRead<T>(read: () => Promise<T>, label: string): Promise<T> {
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

// An upload of *.pdf bytes that are not a PDF: pdfium fails to open it and the worker
// dead-letters at pages_not_rendered (worker.go). EXTR-15-04 gives that kind its own sentence,
// so the needle must be a fragment of THAT sentence and of no other kind's --
// documentRun.test.ts's TS15-10b is this literal's only local oracle.
export const DEAD_LETTER_NEEDLE = 'it may be damaged, or protected by a password'

/**
 * The document journey EXTR09-E2E-01 proves: lands on the document's own extraction review,
 * then takes its exit to the real invoice detail. `file` defaults to the one-page fixture.
 */
export async function extractOneDocument(
  page: Page,
  label: string,
  file: { name: string; buffer: Buffer } = { name: 'native_invoice.pdf', buffer: uniquePdfBytes() },
): Promise<void> {
  const { SHARD, PERSONAS } = shardPersonas(basename(test.info().file))
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

// Opens the review screen and RETURNS the 200 the SPA itself consumed. Every wire fact these
// specs assert comes off this one response -- never a literal copied out of mock.go, which
// would assert the fixture against itself.
export async function openExtractionReview(page: Page): Promise<ExtractionDetail> {
  // Promise.all, not a bare waiter awaited after the click (EXTR-11-09 correction). The waiter
  // is created first either way -- an array literal evaluates left to right, so the response
  // listener is registered before the click starts -- but a click that fails its 30s
  // actionability check used to leave the 120s waiter unhandled, and its rejection surfaced as
  // worker noise ~90s AFTER the test had already reported. Promise.all attaches a handler to
  // both, so the click's failure is the one that propagates and nothing is left dangling.
  //
  // `$`-anchored: the LIST route has no id segment and the page route ends in /pages/{n}.
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

export async function boxOf(page: Page, testid: string): Promise<Rect> {
  const box = await page.getByTestId(testid).boundingBox()
  expect(box, `${testid} did not render`).not.toBeNull()
  return box as Rect
}

// AC-4's ONLY real oracle. `overlapOf(highlight, image) === highlight` is true for a box
// anywhere inside the page and VACUOUSLY true for a 0x0 box entirely off it -- overlapOf
// clamps per axis with Math.max(0, …), so the intersection collapses to the highlight's own
// rect (see overlapOf in layout.ts). Containment therefore rides along as a cheap extra, never as
// the assertion.
//
// Per-axis pixel tolerance, not a strict float compare: the browser resolves `left: 62%` and
// `width: 28%` against the frame independently, so an edge can carry two roundings, and a
// strict toEqual on boundingBox() floats flakes. 1.5px on a 560-960px frame is under 0.3% --
// three orders coarser than the defect this catches (a wrong axis, an inverted origin, or a
// transform, all of which land tens of percent out).
export const RATIO_TOL_PX = 1.5

// The story's Invented-copy table. A transcription, deliberately not an import: the SPA is a
// different package, and reading the mapping out of the module under test would assert it
// against itself. `ambiguous` is not a fixed key here -- its pill's word depends on a live
// chip count.
export const REASON_PILL: Record<Exclude<ExtractionReason, '' | 'ambiguous'>, string> = {
  unreadable: "COULDN'T READ THIS CLEARLY",
  inconsistent: "DOESN'T ADD UP",
  missing: 'NOT FOUND',
}

// internal/extraction/vocabulary.go, HeaderFields -- and the order one Save writes in. A
// transcription, deliberately not an import: the SPA is a different package, and reading the order out of the module under
// test would assert it against itself. EXTR-13-06's headerFields guard in
// frontend/app/src/lib/wireMirrors.test.ts now compares this copy to the Go slice in order, so
// a drift here reds that file rather than passing quietly.
//
// EXTR-13-02 widened the mock's default result with 16 line-item names; EXTR-13-07 gives them
// their own grid, LineItemGrid, and the header pane renders none of them.
export const VOCABULARY = [
  'invoice_number',
  'issue_date',
  'supplier_tin',
  'supplier_name',
  'buyer_tin',
  'buyer_name',
  'currency',
  'subtotal',
  'vat',
  'total',
]

// `POINT_ARMED` replaces the artboard's click-arm string: this build's gesture is a DRAG, and under
// the 24x12 floor a user who obeys "click the words" gets nothing.
export const POINT_ARMED = 'Waiting — drag a box around it on the document'

export function sameRect(a: Rect, b: Rect, slackPx = 1): boolean {
  return (
    Math.abs(a.x - b.x) <= slackPx &&
    Math.abs(a.y - b.y) <= slackPx &&
    Math.abs(a.width - b.width) <= slackPx &&
    Math.abs(a.height - b.height) <= slackPx
  )
}
