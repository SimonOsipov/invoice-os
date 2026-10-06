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

// The shard tenants of specFile; PERSONAS shadows the api client's 1111 / 2222 pair.
export function shardPersonas(specFile: string): { SHARD: ReturnType<typeof shardTenants>; PERSONAS: { A: Persona; B: Persona } } {
  const SHARD = shardTenants(specFile)
  const PERSONAS: { A: Persona; B: Persona } = {
    A: { ...SHARD.a, tenantId: SHARD.a.id },
    B: { ...SHARD.b, tenantId: SHARD.b.id },
  }
  return { SHARD, PERSONAS }
}

// shardPersonas for a spec's absolute path (test.info().file); targets.test.ts covers it.
export function shardPersonasForFile(file: string): ReturnType<typeof shardPersonas> {
  return shardPersonas(basename(file))
}

// Console/pageerror collection, with the approval-run 404 dropped.
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

// Copy of invoice-surfaces.spec.ts's selectEntity. Invoices is client-scoped, so a test must
// select its own fixture entity; signInAs() alone leaves the switcher on clients[0].
export async function selectEntity(page: Page, entityName: string): Promise<void> {
  await page.getByTestId('company-switcher').click()
  await page.getByTestId('company-switcher-option').filter({ hasText: entityName }).click()
}

// The multipart body Chromium recorded. '' when none, so an assertion over it fails loudly.
export function requestBody(req: Request): string {
  return req.postDataBuffer()?.toString('utf8') ?? req.postData() ?? ''
}

export const DOCUMENT_FIXTURES = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents')
// The rich fixture, so wire-derived specs read real field content.
const NATIVE_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'rich_invoice.pdf'))

// Fresh bytes per pick: per-tenant dedupe plus the permanent enqueue key would otherwise
// settle the poll on a previous run's job. A trailing PDF comment keeps `startxref` valid.
export function uniquePdfBytes(): Buffer {
  return Buffer.concat([NATIVE_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// A *.pdf whose bytes are not a PDF: it uploads, then dead-letters in the worker.
// Fresh bytes per call, for the same enqueue-key reason as uniquePdfBytes().
export function uniqueGarbageBytes(): Buffer {
  return Buffer.concat([Buffer.from('not a pdf at all'), Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// The CONTENT box, not the border box, so a row overflowing the card's gutter fails.
// scrollWidth/clientWidth catch text overflowing a stretched flex item.
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

// Two consecutive agreeing reads: one read can land mid-transform. Returns the agreed value.
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

// Fragment of the pages_not_rendered sentence only; documentRun.test.ts TS15-10b is its oracle.
export const DEAD_LETTER_NEEDLE = 'it may be damaged, or protected by a password'

// Upload one PDF, land on its extraction review, then take its exit to the invoice detail.
export async function extractOneDocument(
  page: Page,
  label: string,
  file: { name: string; buffer: Buffer } = { name: 'native_invoice.pdf', buffer: uniquePdfBytes() },
): Promise<void> {
  const { SHARD, PERSONAS } = shardPersonasForFile(test.info().file)
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

// Opens the review screen and returns the 200 the SPA consumed; wire facts come off it, never
// off a literal copied from mock.go.
export async function openExtractionReview(page: Page): Promise<ExtractionDetail> {
  // Promise.all so a failed click rejects the test instead of leaving the waiter unhandled.
  // `$`-anchored: the list route has no id segment and the page route ends in /pages/{n}.
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

// Per-axis pixel tolerance: `left: 62%` and `width: 28%` resolve independently, so edges can
// carry two roundings. Containment alone is vacuously true for a 0x0 box off the page.
export const RATIO_TOL_PX = 1.5

// Transcribed from the story's copy table, not imported from the SPA. `ambiguous` has no fixed
// key: its pill depends on a live chip count.
export const REASON_PILL: Record<Exclude<ExtractionReason, '' | 'ambiguous'>, string> = {
  unreadable: "COULDN'T READ THIS CLEARLY",
  inconsistent: "DOESN'T ADD UP",
  missing: 'NOT FOUND',
}

// internal/extraction/vocabulary.go HeaderFields, in Save order; transcribed, not imported.
// wireMirrors.test.ts's headerFields guard compares it to the Go slice.
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

// This build's gesture is a drag, not a click.
export const POINT_ARMED = 'Waiting — drag a box around it on the document'

export function sameRect(a: Rect, b: Rect, slackPx = 1): boolean {
  return (
    Math.abs(a.x - b.x) <= slackPx &&
    Math.abs(a.y - b.y) <= slackPx &&
    Math.abs(a.width - b.width) <= slackPx &&
    Math.abs(a.height - b.height) <= slackPx
  )
}
