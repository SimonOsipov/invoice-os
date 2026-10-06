// The second half of the import-wizard deployed specs (from EXTR11-E2E-11), run as matrix leg
// `import-wizard-2` on its own tenants. The split is by time; helpers shared with
// import-wizard.spec.ts live in importWizardShared.ts.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { test, expect, type Locator, type Page, type Response, type Route } from '@playwright/test'
import {
  login,
  apiBase,
  createEntity,
  createInvoice,
  listInvoices,
  getAuditLog,
  getCarriedReading,
  getExtractions,
  getExtractionDetail,
  getInvoice,
  postFieldCorrection,
  rawFetch,
  type CorrectionResponse,
  type ExtractionCandidate,
  type ExtractionDetail,
  type ExtractionJob,
  type ExtractionRegion,
} from '../api/client'
import { ensureFirmPolicyActive } from '../api/contract-helpers'
import { freshTin } from '../api/fixtures'
import { expectedStatusDropper } from './consoleGate'
import { assertPageDoesNotScrollSideways, gaps, overlapOf, rectsOverlap, WIDE_WIDTHS, type Rect } from './layout'
import { signInAs } from '../personaSession'
import {
  boxOf,
  collectErrors,
  DEAD_LETTER_NEEDLE,
  DOCUMENT_FIXTURES,
  edgesOf,
  extractOneDocument,
  openExtractionReview,
  POINT_ARMED,
  RATIO_TOL_PX,
  REASON_PILL,
  requestBody,
  sameRect,
  selectEntity,
  settledRead,
  shardPersonas,
  uniqueGarbageBytes,
  uniquePdfBytes,
  VOCABULARY,
} from './importWizardShared'
import {
  buildHeaderOnlyCsv,
  buildMixedCsv,
  buildSingleInvoiceCsv,
} from '../importFixtures'

const { SHARD, PERSONAS } = shardPersonas('import-wizard-2.spec.ts')

test.beforeAll(async () => {
  expect(test.info().project.name, 'import-wizard-2.spec.ts belongs to the import-wizard-2 shard').toBe('import-wizard-2')
  await ensureFirmPolicyActive(await login(PERSONAS.A))
})

// Committed by EXTR-09-03. Image-only scan, no text layer -- settles document_text_layer =
// unreadable. Same recipe, same permanent-enqueue-key reason as uniquePdfBytes().
const SCANNED_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'scanned_invoice.pdf'))

function uniqueScannedPdfBytes(): Buffer {
  return Buffer.concat([SCANNED_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// Committed by EXTR-09-03. Image-only like SCANNED_INVOICE_PDF, but OCR-readable -- the pair
// proves "no text layer" and "unreadable" are not the same verdict. Same recipe.
const DENSE_INVOICE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'dense_invoice.pdf'))

function uniqueDensePdfBytes(): Buffer {
  return Buffer.concat([DENSE_INVOICE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// A copy of internal/extraction/testdata/advisory_register.pdf (fxE2ECopies). Same recipe.
const ADVISORY_REGISTER_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'advisory_register.pdf'))

function uniqueAdvisoryRegisterPdfBytes(): Buffer {
  return Buffer.concat([ADVISORY_REGISTER_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// Copies of internal/extraction/testdata/chrome_register.pdf and its twin (fxE2ECopies). Both
// share advisory_register.pdf's fingerprint by design (EXTR-36) -- see the ordering note on
// EXTR36-E2E-02 below. Same recipe.
const CHROME_REGISTER_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'chrome_register.pdf'))

const CHROME_REGISTER_TWIN_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'chrome_register_twin.pdf'))

function uniqueChromeRegisterPdfBytes(): Buffer {
  return Buffer.concat([CHROME_REGISTER_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

function uniqueChromeRegisterTwinPdfBytes(): Buffer {
  return Buffer.concat([CHROME_REGISTER_TWIN_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// AIR-03-05's deployed-steering fixture (fxE2ECopies): the AIFAKE-ANSWER marker steers the fake
// fleet's document reading. Same recipe as the others above.
const AI_STEERED_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'ai_steered_invoice.pdf'))

function uniqueAiSteeredPdfBytes(): Buffer {
  return Buffer.concat([AI_STEERED_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// AIR-04-04's deployed fixture (fxE2ECopies): AIFAKE-UNAVAILABLE makes the fake fleet's AI call fail.
const AI_UNAVAILABLE_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'ai_unavailable_invoice.pdf'))

function uniqueAiUnavailablePdfBytes(): Buffer {
  return Buffer.concat([AI_UNAVAILABLE_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// fxJevDoubt (fxE2ECopies): JEVFAKE-DOUBT makes the fake Jev doubt the one checked field.
const JEV_DOUBT_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'jev_doubt_invoice.pdf'))

function uniqueJevDoubtPdfBytes(): Buffer {
  return Buffer.concat([JEV_DOUBT_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// fxJevReceipt (fxE2ECopies): JEVFAKE-CHOICE-cmVjZWlwdA makes the fake Jev answer `receipt`.
const JEV_RECEIPT_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'jev_receipt_invoice.pdf'))

function uniqueJevReceiptPdfBytes(): Buffer {
  return Buffer.concat([JEV_RECEIPT_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// AIR-08-13's deployed fixture (fxE2ECopies): a ruled 2-row table plus an AIFAKE-LINES-ANSWER
// marker steering three line-item rows. Same recipe as the others above.
const AI_LINES_PDF = readFileSync(join(DOCUMENT_FIXTURES, 'ai_lines_invoice.pdf'))

function uniqueAiLinesPdfBytes(): Buffer {
  return Buffer.concat([AI_LINES_PDF, Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8')])
}

// No new committed fixture: reuses SCANNED_INVOICE_PDF, uniqueScannedPdfBytes()'s recipe, plus a
// second trailing comment steering the fake's image-read answer.
const IMAGE_READ_ANSWER = {
  invoice_number: 'INV-5520',
  issue_date: '2026-07-14',
  supplier_tin: null,
  supplier_name: null,
  buyer_tin: '9999999-1202',
  buyer_name: null,
  currency: null,
  subtotal: null,
  vat: null,
  total: '1935.00',
}

function uniqueImageSteeredPdfBytes(): Buffer {
  return Buffer.concat([
    SCANNED_INVOICE_PDF,
    Buffer.from(`%e2e-${crypto.randomUUID()}\n`, 'utf8'),
    Buffer.from(`%AIFAKE-ANSWER-${Buffer.from(JSON.stringify(IMAGE_READ_ANSWER)).toString('base64url')}\n`, 'utf8'),
  ])
}

// A correction applies to the invoice filed from the document, which commits AFTER the
// extraction job reports succeeded -- posting on that signal alone races it and 409s
// (ErrNoInvoiceForDocument, handlers.go).
async function settledInvoiceFor(token: string, entityId: string): Promise<void> {
  await expect
    .poll(async () => (await listInvoices(token, { entity_id: entityId, limit: 5 })).invoices.length, {
      message: 'no invoice was filed from the register -- a correction would have nothing to apply to',
      timeout: 120_000,
      intervals: [1_000],
    })
    .toBeGreaterThan(0)
}

// The canonical DOCX type, on both sides of the wire: ACCEPTED_PICKED_TYPES (lib/importFlow.ts)
// and acceptedDocumentTypes (internal/extraction/classify.go) carry this exact spelling.
const DOCX_MIME = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'

// A byte-for-byte copy of internal/extraction/testdata/invoice.docx, the fixture the Go
// golden pins (corpus_wired_db_test.go: ASC-2026-0919 / 2026-08-14 / 4300.00). That golden
// was recorded through a LOCAL docling; EXTR15-E2E-03 is the first read of these bytes by
// the DEPLOYED sidecar.
const GOLDEN_INVOICE_DOCX = readFileSync(join(DOCUMENT_FIXTURES, 'golden_invoice.docx'))

// Fresh bytes per pick, same permanent-enqueue-key reason as uniquePdfBytes() -- but a zip
// cannot take a trailing comment the way a PDF can. What moves the content hash here is the
// zip's OWN end-of-central-directory comment: the last 22 bytes are the EOCD record, its
// comment-length field is the final two, and every reader finds the EOCD by scanning back for
// its signature. The archive stays readable and every member's offset is untouched.
// deployedProofGuards.test.ts unzips a freshened copy and reads the golden's number back out.
function uniqueGoldenDocxBytes(): Buffer {
  const comment = Buffer.from(`e2e-${crypto.randomUUID()}`, 'utf8')
  const out = Buffer.concat([GOLDEN_INVOICE_DOCX, comment])
  out.writeUInt16LE(comment.length, GOLDEN_INVOICE_DOCX.length - 2)
  return out
}

// A well-formed but EMPTY zip named *.docx -- invoice-surfaces.spec.ts's extr09Docx recipe,
// rebuilt here because importing it would re-register that file's own tests. 22 bytes of EOCD
// (PK\x05\x06 then 16 zero bytes) plus a comment that carries the uniqueness. A DOCX is
// boxless, so the worker skips the render entirely (EXTR-15-02) and this reaches the reader,
// which cannot convert it: docling answers 422 and the job dead-letters at text_not_read.
function uniqueEmptyDocxBytes(): Buffer {
  const comment = Buffer.from(`e2e-${crypto.randomUUID()}`, 'utf8')
  const out = Buffer.alloc(22 + comment.length)
  out.set([0x50, 0x4b, 0x05, 0x06], 0)
  out.writeUInt16LE(comment.length, 20)
  out.set(comment, 22)
  return out
}

// Steps the viewport down until the pane stops shrinking and returns that viewport width.
// A pane that never shrank is pinned, not floored, so that fails too.
async function descendToPaneFloor(
  page: Page,
  pane: Locator,
): Promise<{ floorWidth: number; descent: { width: number; paneWidth: number }[] }> {
  const descent: { width: number; paneWidth: number }[] = []
  let shrank = false
  for (let width = 1280; width >= 1000; width -= 40) {
    await page.setViewportSize({ width, height: 1080 })
    const paneWidth = await settledRead(async () => (await pane.boundingBox())?.width ?? 0, `pane width at ${width}px`)
    expect(paneWidth, `the pane has no width at ${width}px`).toBeGreaterThan(0)
    const previous = descent.at(-1)
    descent.push({ width, paneWidth })
    if (!previous) continue
    if (Math.abs(previous.paneWidth - paneWidth) <= 1) {
      expect(shrank, `the pane never shrank before it stopped at ${width}px: ${JSON.stringify(descent)}`).toBe(true)
      return { floorWidth: width, descent }
    }
    if (previous.paneWidth - paneWidth > 1) shrank = true
  }
  throw new Error(`the pane never stopped shrinking at or above 1000px: ${JSON.stringify(descent)}`)
}

// --- EXTR11-E2E-11 · frame centring and point-button fill

test('EXTR11-E2E-11 (AC-8): the page frame centres in its column and the point button fills its cell', async ({ page }) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-11-09 frame')
  await openExtractionReview(page)

  // The frame's band scales with zoom, so whether it fits its column at 1920 depends on zoom 100.
  await expect(page.getByTestId('extraction-zoom-100'), 'the centring below is measured at zoom 100').toHaveAttribute(
    'aria-pressed',
    'true',
  )

  // Chrome resolves `margin: 0 auto` to used values, so the claim is that the two agree. They
  // agree only where the frame fits its column (CSS 2.1 10.3.3), hence the 1920 window.
  const entryViewport = page.viewportSize()
  await page.setViewportSize({ width: 1920, height: 1080 })
  const frame = await settledRead(
    () =>
      page.evaluate(() => {
        const el = document.querySelector('[data-testid="extraction-page-1"]')
        const pad = document.querySelector('[data-testid="extraction-ground"] > div')
        if (!el || !pad) return null
        const cs = getComputedStyle(el)
        return {
          marginLeft: cs.marginLeft,
          marginRight: cs.marginRight,
          frameWidth: el.getBoundingClientRect().width,
          padWidth: pad.getBoundingClientRect().width,
        }
      }),
    'the page frame margins at 1920px',
  )
  expect(frame, 'the frame and its column must both render at 1920px').not.toBeNull()
  expect(
    frame!.frameWidth,
    `the frame still overflows its column at 1920px (frame ${frame!.frameWidth}, column ${frame!.padWidth}), so the centring below would pin CSS 2.1 10.3.3's overconstraint rule rather than 'margin: 0 auto'`,
  ).toBeLessThanOrEqual(frame!.padWidth + 1)
  expect(
    frame!.marginLeft,
    `the frame's auto margins disagree (left ${frame!.marginLeft}, right ${frame!.marginRight}) -- 'margin: 0 auto 18px' is what centres it`,
  ).toBe(frame!.marginRight)
  if (entryViewport) await page.setViewportSize(entryViewport)

  // The button declares no width; the cell's flex column stretches it to the content box.
  await expect(page.getByTestId('extraction-point-buyer_tin'), 'the point button must render').toBeVisible({ timeout: 30_000 })
  const point = await page.evaluate(() => {
    const button = document.querySelector('[data-testid="extraction-point-buyer_tin"]')
    const cell = document.querySelector('[data-testid="extraction-field-buyer_tin"]')
    if (!button || !cell) return null
    const cs = getComputedStyle(cell)
    return {
      buttonWidth: button.getBoundingClientRect().width,
      cellContentWidth: (cell as HTMLElement).clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight),
    }
  })
  expect(point, 'the point button and its cell must both render, or the width claim compares nothing').not.toBeNull()
  expect(point!.cellContentWidth, 'the buyer_tin cell has no content width').toBeGreaterThan(0)
  expect(
    Math.abs(point!.buttonWidth - point!.cellContentWidth),
    `the point button is ${point!.buttonWidth}px inside a ${point!.cellContentWidth}px cell content box`,
  ).toBeLessThanOrEqual(1)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-12-09 · the settle-every-field journey, and the pane's floor --------------------
//
// Neither row can execute before this subtask marks the PR ready: `dev-env.yml` gates deploy and
// E2E on `pull_request.draft == false`. The local oracle is `pnpm -r typecheck` plus
// `playwright test --list`.

// The header vocabulary this block reads -- HeaderFields, in the order one Save writes in --
// is `VOCABULARY`, declared once in importWizardShared.ts.

// internal/extraction/handlers_correction.go, lockedFields: a correction on any of the three is
// a 422 while it is unflagged, which the steered fixture never lifts (AIR-03-06), so none of
// them is what this journey types over.
const LOCKED_FIELDS = ['invoice_number', 'supplier_tin', 'supplier_name']

// The story's Invented-copy table, per method (artboard `:639-641`).
const CHANGED_LABEL: Record<string, string> = {
  typed: 'YOU CHANGED THIS',
  pointed: 'YOU POINTED THIS OUT',
  chosen: 'YOU CHOSE THIS',
}

test('EXTR12-E2E-06 (AC-3/AC-5): choose, type and point settle three fields, and the register and the audit log agree', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  // The ONE-page fixture: the point surface only needs a page, and EXTR12-E2E-05 already owns
  // the page-2 indexing claim.
  await extractOneDocument(page, 'EXTR-12-09 journey')
  const detail = await openExtractionReview(page)

  // Every subject off the wire the SPA itself consumed, never mock.go, and every floor first: a
  // fixture change that flattened the mock would otherwise leave this journey settling nothing
  // and calling it three corrections.
  const ambiguous = detail.fields.find((f) => f.reason === 'ambiguous')
  expect(ambiguous, 'this document reported no ambiguous field -- there is no candidate to choose').toBeTruthy()
  expect(
    ambiguous!.alternatives.length,
    `${ambiguous!.name} carries no alternative -- the chip this journey clicks does not exist`,
  ).toBeGreaterThanOrEqual(1)
  const chosenValue = ambiguous!.alternatives[0].value
  expect(chosenValue, `${ambiguous!.name}'s first alternative carries no value`).toBeTruthy()
  // Chip 1, never chip 0: chip 0 is the decided reading, whose value equals the wire, so
  // choosing it exercises the keep-a-no-op arm and leaves the value unmoved. This journey needs
  // the value to MOVE.
  expect(
    chosenValue,
    'the first alternative reads the same as the decided one, so choosing it would move nothing',
  ).not.toBe(ambiguous!.value)

  const missing = detail.fields.find((f) => f.reason === 'missing')
  expect(missing, 'this document reported no missing field -- there is nothing to point at').toBeTruthy()
  expect(missing!.value, 'the missing field already carries a value').toBeNull()
  expect(missing!.region, 'the missing field already carries a region, so a drawn box proves nothing').toBeNull()

  // By NAME, the way EXTR12-E2E-02 picks `total` --
  // never by position. The wire is ordered by field_name (created_at defaults to now() and
  // writeFieldResultsTx writes a job's rows on ONE transaction, so reader.go's ORDER BY
  // degenerates to the name), and EXTR-13-02's line cells sort ahead of `subtotal`: "the first
  // writable inconsistent field" resolves to line_items[2].line_total, which has no header cell
  // to type into and which refuseField would 422. `subtotal` sorts ahead of `total`, and since
  // EXTR-23 put `total` in the doubt scope it renders a chooser rather than an input, so it
  // offers nothing to type into either. The two properties that pick made implicit are asserted here
  // instead of assumed, so a build that locked `subtotal` or stopped flagging it reds on the wire
  // rather than at the POST. TestExtractionDetail_MockDefaultArrivesInFieldNameOrder pins the
  // ordering from the Go side.
  const typed = detail.fields.find((f) => f.name === 'subtotal')
  expect(typed, 'the wire the screen read carries no `subtotal` -- there is nothing to type over').toBeTruthy()
  expect(LOCKED_FIELDS, 'subtotal is locked now, so the correction this journey posts would be a 422').not.toContain(
    typed!.name,
  )
  expect(
    typed!.reason,
    'subtotal is not flagged inconsistent, so this journey types over a field that never asked to be settled',
  ).toBe('inconsistent')

  const names = [ambiguous!.name, missing!.name, typed!.name]
  expect(new Set(names).size, `two of the three subjects are the same field: ${names.join(', ')}`).toBe(3)
  for (const name of names) expect(VOCABULARY, `${name} is outside the header vocabulary`).toContain(name)

  // What one Save owes, in the vocabulary's own order -- the order savableCorrections sorts into
  // so the append-only table's seq follows the order the person reads.
  const plan = [
    { field: ambiguous!.name, method: 'chosen' },
    { field: missing!.name, method: 'pointed' },
    { field: typed!.name, method: 'typed' },
  ].sort((a, b) => VOCABULARY.indexOf(a.field) - VOCABULARY.indexOf(b.field))

  const save = page.getByTestId('extraction-save')

  // -- 1. CHOOSE ----------------------------------------------------------------------------
  const chips = page.locator(`[data-testid^="extraction-chip-${ambiguous!.name}-"]`)
  // W-3: the decided reading is itself a chip, so the row carries N + 1.
  await expect(chips, 'the ambiguous field rendered no chip row').toHaveCount(ambiguous!.alternatives.length + 1)
  await page.getByTestId(`extraction-chip-${ambiguous!.name}-1`).click()
  await expect(
    page.getByTestId(`extraction-chip-${ambiguous!.name}-1`),
    'the clicked chip is not the current one',
  ).toHaveAttribute('aria-current', 'true')
  await expect(
    page.getByTestId(`extraction-chip-${ambiguous!.name}-0`),
    'the decided reading is still current after another candidate was chosen',
  ).toHaveAttribute('aria-current', 'false')

  // -- 2. TYPE ------------------------------------------------------------------------------
  // Plain digits, no thousands separator: invoiceEditFor writes `$n::text::numeric` and
  // '2,468.00'::numeric raises 22P02 -> ErrValueRefused -> 400.
  const TYPED_VALUE = '2468.00'
  await page.getByTestId(`extraction-input-${typed!.name}`).fill(TYPED_VALUE)
  await expect(save, 'a chosen candidate and a typed value left Save disabled').toBeEnabled()

  // -- 3. POINT -----------------------------------------------------------------------------
  const pointButton = page.getByTestId(`extraction-point-${missing!.name}`)
  await expect(pointButton, `${missing!.name} offers no way to point at it`).toBeVisible({ timeout: 30_000 })
  await pointButton.click()
  await expect(pointButton, 'clicking the point button changed nothing the reader can see').toHaveText(POINT_ARMED, {
    timeout: 15_000,
  })

  await expect(page.getByTestId('extraction-point-surface-1'), 'the armed field takes no drag').toBeVisible({
    timeout: 15_000,
  })
  const s = await boxOf(page, 'extraction-point-surface-1')
  // Comfortably over the 24x12 floor at every frame size in the band.
  await page.mouse.move(s.x + 0.3 * s.width, s.y + 0.25 * s.height)
  await page.mouse.down()
  await page.mouse.move(s.x + 0.62 * s.width, s.y + 0.55 * s.height, { steps: 8 })
  await page.mouse.up()

  const highlight = page.getByTestId('extraction-highlight')
  // applyDraft without its `pointed` arm renders NO highlight, because the missing field's wire
  // region is null. That build reds here.
  await expect(highlight, 'the drawn box highlights nothing').toHaveCount(1)
  await expect(highlight, 'the highlight is not the box drawn on the missing field').toHaveAttribute(
    'data-snip',
    missing!.name,
  )

  // -- 4. FILL THE POINT --------------------------------------------------------------------
  // Required, not decorative: savableCorrections drops a blank pointed entry
  // (extractionReview.ts), so without this the box never reaches the wire.
  const POINTED_VALUE = '31775208-0003'
  await page.getByTestId(`extraction-input-${missing!.name}`).fill(POINTED_VALUE)

  // -- 5. SAVE ------------------------------------------------------------------------------
  // Registered BEFORE the click, and .json() called inside the handler so no body is read after
  // the fact.
  const corrections: {
    field: string
    method: string
    hasRegion: boolean
    status: number
    body: Promise<CorrectionResponse | null>
  }[] = []
  page.on('response', (r) => {
    const req = r.request()
    // POST, not merely non-GET: the SPA and the gateway are separate origins, so every one of
    // these is preceded by a CORS preflight OPTIONS carrying no body at all.
    if (req.method() !== 'POST') return
    const path = new URL(r.url()).pathname
    if (!path.endsWith('/corrections')) return
    const sent = (req.postDataJSON() ?? {}) as { method?: string; region?: ExtractionRegion | null }
    corrections.push({
      field: decodeURIComponent(path.split('/fields/')[1]?.split('/')[0] ?? ''),
      method: sent.method ?? '',
      hasRegion: sent.region !== null && sent.region !== undefined,
      status: r.status(),
      body: (r.json() as Promise<CorrectionResponse>).catch(() => null),
    })
  })

  const [reread] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 120_000 },
    ),
    save.click(),
  ])
  expect(reread.status(), 'the post-save re-read failed').toBe(200)

  // Settled before it is read: the re-read is the LAST request the Save makes, but a response
  // event and the promise that resolves on it are dispatched independently. A fourth POST
  // arriving after this poll is still caught by the equality below.
  await expect
    .poll(() => corrections.length, { message: 'the Save did not post three corrections', timeout: 15_000 })
    .toBe(3)

  // Exactly three POSTs, in vocabulary order, each 201. Firing them in parallel reds the order;
  // two POSTs reds the count.
  expect(
    corrections.map((c) => c.field),
    'the Save did not post the three settled fields in vocabulary order',
  ).toEqual(plan.map((p) => p.field))
  expect(
    corrections.map((c) => c.method),
    'the methods did not follow the fields they were posted for',
  ).toEqual(plan.map((p) => p.method))
  expect(corrections.map((c) => c.status), 'a correction was refused').toEqual([201, 201, 201])

  // Only the pointed body carries a region: a chosen request with one is a 400
  // (msgRegionDisagrees), so this is the deployed half of that rule.
  expect(
    corrections.filter((c) => c.hasRegion).map((c) => c.field),
    'a correction other than the pointed one carried a region',
  ).toEqual([missing!.name])

  // The invoice comes off the 201 bodies (CorrectionResponse.invoice_id), never guessed.
  const bodies = await Promise.all(corrections.map((c) => c.body))
  const invoiceIds = new Set(bodies.map((b) => b?.invoice_id ?? ''))
  expect(invoiceIds.size, 'the three corrections named different invoices').toBe(1)
  const invoiceId = [...invoiceIds][0]
  expect(invoiceId, 'no correction body named an invoice, so the audit read below scopes to nothing').toMatch(
    /^[0-9a-fA-F-]{36}$/,
  )

  // -- What the screen says afterwards ------------------------------------------------------
  //
  // Count AND identity: a build that marked every field reds the count, and a build whose
  // correctedMarker picks one arm for all three reds two labels.
  await expect(
    page.locator('[data-testid^="extraction-marker-"]'),
    'the settled fields did not each take exactly one marker',
  ).toHaveCount(3)
  // And the namespace resolves to those three BY NAME. EXTR-13-02 put 15 line-item cells into
  // the pane's render, and a marker renders only over a correction, so this is the assertion
  // that the widened fixture left the `extraction-marker-` sweep where it was: a count alone
  // would still read 3 if a line cell took a marker while a settled field lost its own.
  const markerNames = (
    await page
      .locator('[data-testid^="extraction-marker-"]')
      .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.testid ?? ''))
  )
    .map((id) => id.replace('extraction-marker-', ''))
    .sort()
  expect(markerNames, 'the marker namespace resolved to fields other than the three that were settled').toEqual(
    plan.map((p) => p.field).sort(),
  )
  for (const p of plan) {
    await expect(page.getByTestId(`extraction-marker-${p.field}`), `${p.field} settled and shows no marker`).toBeVisible()
    await expect(
      page.getByTestId(`extraction-field-${p.field}`).getByText(CHANGED_LABEL[p.method], { exact: true }),
      `${p.field} was ${p.method} and does not say "${CHANGED_LABEL[p.method]}"`,
    ).toBeVisible()
  }

  // A settled field stops shouting: reader.go clears Reason and empties Alternatives on a
  // corrected field, so the chip row goes and the input takes its place.
  await expect(
    page.locator(`[data-testid^="extraction-chip-${ambiguous!.name}-"]`),
    'the settled ambiguous field still offers its chips',
  ).toHaveCount(0)
  await expect(
    page.getByTestId(`extraction-input-${ambiguous!.name}`),
    'the settled field holds a value other than the candidate that was chosen',
  ).toHaveValue(chosenValue as string)
  await expect(page.getByTestId(`extraction-input-${missing!.name}`)).toHaveValue(POINTED_VALUE)
  await expect(page.getByTestId(`extraction-input-${typed!.name}`)).toHaveValue(TYPED_VALUE)

  // -- The audit rows -----------------------------------------------------------------------
  //
  // Filtered on the EVENT and matched on the payload, never `invoice_id`: audit_log.invoice_id
  // is a GENERATED column whose CASE lists no extraction event
  // (migrations/20260822080722_audit_log_invoice_id_column_and_index.sql), so an invoice-scoped
  // read of these rows is empty by construction. The control read below asserts exactly that.
  const scoped = await getAuditLog(token, { event: ['extraction.field_corrected'], limit: 100 })
  const mine = scoped.events.filter((e) => (e.payload as { invoice_id?: string }).invoice_id === invoiceId)
  expect(
    mine.length,
    `this invoice recorded ${mine.length} extraction.field_corrected events, not the three the Save posted`,
  ).toBe(3)
  expect(
    mine.map((e) => (e.payload as { field?: string }).field).sort(),
    'the audit payloads name other fields than the three that were settled',
  ).toEqual(plan.map((p) => p.field).sort())
  expect(
    mine.map((e) => (e.payload as { method?: string }).method).sort(),
    'the audit payloads record other methods than the three that were used',
  ).toEqual(plan.map((p) => p.method).sort())
  // migrations/20260829195203_audit_log_entity_for_extraction.sql resolves these rows to a
  // company THROUGH the invoice they correct; a NULL entity_id is the workspace-level spelling
  // and would misfile a client action as firm-wide.
  for (const e of mine) {
    expect(e.entity_id, `an extraction.field_corrected row was filed workspace-wide (${e.id})`).not.toBeNull()
  }

  // The event filter's own non-vacuity control: the tenant-wide log must be strictly larger, or
  // "exactly three" would also be true of a reader that returned everything it had.
  const wholeLog = await getAuditLog(token, { limit: 1 })
  expect(
    wholeLog.total,
    `the workspace log holds ${wholeLog.total} events and the extraction filter ${scoped.total} -- the filter narrowed nothing`,
  ).toBeGreaterThan(scoped.total)

  // The reason the filter is on the event. invoice-surfaces.spec.ts pins the same fact from the
  // UI side ("Document and extraction events are recorded against the workspace, not against a
  // single invoice"), and a build that added the event to the generated column reds both.
  const byInvoice = await getAuditLog(token, {
    invoice_id: invoiceId,
    event: ['extraction.field_corrected'],
    limit: 100,
  })
  expect(
    byInvoice.events.length,
    'audit_log.invoice_id now resolves an extraction correction -- invoice-surfaces.spec.ts still claims it cannot',
  ).toBe(0)

  await testInfo.attach('extraction-settle-every-field.json', {
    body: JSON.stringify(
      {
        plan,
        chosenValue,
        typedValue: TYPED_VALUE,
        pointedValue: POINTED_VALUE,
        invoiceId,
        posted: corrections.map((c) => ({ field: c.field, method: c.method, hasRegion: c.hasRegion, status: c.status })),
        audit: mine.map((e) => ({ id: e.id, event: e.event, entityId: e.entity_id, payload: e.payload })),
        auditTotals: { extractionEvents: scoped.total, wholeLog: wholeLog.total, invoiceScoped: byInvoice.events.length },
        notCovered: ['Undo (method=undone)', 'a partial-failure Save', "AA-22b's chosen-no-op arm"],
      },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR12-E2E-07 (AC-4, W-6): the fields pane keeps its floor and its two columns, and no cell spills at the floor', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-12-09 floor')
  const detail = await openExtractionReview(page)
  expect(detail.fields.length, 'no field on this document -- every measurement below is vacuous').toBeGreaterThan(0)

  // EXTR-13-07: the fields pane renders the header vocabulary only -- a line-item cell has its
  // own grid, LineItemGrid -- so every rendered-row count below is bounded to it.
  const wireNamesB = detail.fields.map((f) => f.name)
  const headerNamesB = wireNamesB.filter((n) => !n.startsWith('line_items'))
  expect(headerNamesB.length, 'no header field on this document -- every measurement below is vacuous').toBeGreaterThan(0)

  const pane = page.getByTestId('extraction-fields')
  const shellBody = page.getByTestId('extraction-review-body')
  // The pane's two children, in the artboard's order (`:225` header over `:230` body) --
  // EXTR11-E2E-02b resolves the scroller the same way, and the body is the only scroller.
  const paneBody = pane.locator('> div').nth(1)
  const cells = page.locator('[data-testid^="extraction-field-"]')

  type Wide = {
    width: number
    pane: Rect
    body: Rect
    paneGaps: { left: number; right: number }
    columns: number[]
    cells: number
    scrollWidth: number
    clientWidth: number
  }
  const measured: Wide[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first (WIDE_WIDTHS' own order, layout.ts).
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [p, b] = await Promise.all([pane.boundingBox(), shellBody.boundingBox()])
        const scroller = await paneBody.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        // The track count read from GEOMETRY, not from a stylesheet: the grid div carries no
        // testid, and a structural selector into it would silently retarget.
        const xs = await cells.evaluateAll((els) => els.map((el) => Math.round(el.getBoundingClientRect().x)))
        return { p, b, scroller, xs }
      }, `fields pane geometry at ${width}px`)

      expect(m.p && m.b, `the fields pane and the shell body must both render at ${width}px`).toBeTruthy()
      expect(m.p!.width, `the fields pane has no width at ${width}px`).toBeGreaterThan(0)

      // 1. Inside the shell body on BOTH edges -- gaps()'s rule.
      const g = gaps(m.p as Rect, m.b as Rect)
      expect(g.left, `the fields pane passes the body's left edge by ${-g.left}px at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the fields pane passes the body's right edge by ${-g.right}px at ${width}px`).toBeGreaterThanOrEqual(-1)

      // 2. Two columns, exactly. A collapsed one-column grid reports 1, a three-track grid 3,
      //    and nothing else in this suite asserts the track count.
      expect(m.xs.length, `no field cell measured at ${width}px`).toBe(headerNamesB.length)
      const columns = [...new Set(m.xs)].sort((a, b) => a - b)
      expect(columns.length, `the grid reports ${columns.length} column(s) at ${width}px, not two`).toBe(2)

      // 3. The pane's own scroller has nothing to scroll sideways. `overflow-y: auto` with
      //    `overflow-x: visible` computes to `overflow-x: auto`, so the body IS a scroll
      //    container on both axes and its scrollWidth is well defined.
      expect(m.scroller.clientWidth, `the pane body has no width at ${width}px`).toBeGreaterThan(0)
      expect(
        m.scroller.scrollWidth,
        `the pane body scrolls ${m.scroller.scrollWidth - m.scroller.clientWidth}px sideways at ${width}px`,
      ).toBeLessThanOrEqual(m.scroller.clientWidth + 1)

      measured.push({
        width,
        pane: m.p as Rect,
        body: m.b as Rect,
        paneGaps: g,
        columns,
        cells: m.xs.length,
        scrollWidth: m.scroller.scrollWidth,
        clientWidth: m.scroller.clientWidth,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  // -- W-6: the label strip AT the pane's floor ---------------------------------------------
  //
  // NO LOCAL ORACLE. The overflow is a text-measurement fact, so it needs the deployed build's
  // real IBM Plex Mono and Manrope; this row first executes on the deploy gate. The declaration
  // half is ExtractionFields.test.tsx, "wraps the label strip".
  //
  // Measured before the fix, in Chromium at the floor's own geometry (470 - 40 body padding -
  // 16 grid gap, halved, - 16 cell padding = 191px of cell content): the pill overhung
  // `issue_date` by 7.84px and `vat` by 6.02px, and by 15.34 / 13.52 with CI's classic
  // scrollbar in the pane body. `flexWrap: 'wrap'` on LABEL_STRIP is what closed it.
  //
  // The pane is NOT at its floor at any WIDE_WIDTHS -- both panes are flex siblings and the
  // shrink is proportional -- so this descends until it is.
  try {
    const { floorWidth } = await descendToPaneFloor(page, pane)

    // Every cell, not only the two predicted offenders: the walk is cheap and a wrong build
    // spills wherever its copy is longest.
    const spill = await page.evaluate(() => {
      const out: {
        testid: string
        scrollWidth: number
        clientWidth: number
        worst: { node: string; outLeft: number; outRight: number }
      }[] = []
      for (const cell of Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-field-"]'))) {
        const c = cell.getBoundingClientRect()
        let worst = { node: '', outLeft: 0, outRight: 0 }
        for (const el of Array.from(cell.querySelectorAll<HTMLElement>('*'))) {
          const r = el.getBoundingClientRect()
          // A rect collapsed on both axes is inside anything and would pass vacuously.
          if (r.width === 0 && r.height === 0) continue
          const outLeft = c.left - r.left
          const outRight = r.right - c.right
          if (Math.max(outLeft, outRight) > Math.max(worst.outLeft, worst.outRight)) {
            worst = { node: el.dataset.testid ?? el.tagName.toLowerCase(), outLeft, outRight }
          }
        }
        out.push({ testid: cell.dataset.testid ?? '', scrollWidth: cell.scrollWidth, clientWidth: cell.clientWidth, worst })
      }
      return out
    })

    expect(spill.length, `no field cell measured at the ${floorWidth}px floor`).toBe(headerNamesB.length)
    for (const cell of spill) {
      expect(cell.clientWidth, `${cell.testid} has no width at the floor -- its edges are vacuous`).toBeGreaterThan(0)
      expect(
        cell.worst.outLeft,
        `${cell.testid}: ${cell.worst.node} starts ${cell.worst.outLeft.toFixed(2)}px left of its cell at ${floorWidth}px`,
      ).toBeLessThanOrEqual(1)
      expect(
        cell.worst.outRight,
        `${cell.testid}: ${cell.worst.node} ends ${cell.worst.outRight.toFixed(2)}px right of its cell at ${floorWidth}px`,
      ).toBeLessThanOrEqual(1)
      // A second and independent instrument. Verified in Chromium that scrollWidth DOES report
      // inline-end overflow on an `overflow: visible` box, which CSSOM's step 3 would not
      // predict -- so this is measured behaviour, not a spec reading.
      expect(
        cell.scrollWidth,
        `${cell.testid} holds ${cell.scrollWidth}px of content in a ${cell.clientWidth}px box at ${floorWidth}px`,
      ).toBeLessThanOrEqual(cell.clientWidth + 1)
    }

    await testInfo.attach('extraction-fields-pane-floor.json', {
      body: JSON.stringify({ wide: measured, floorWidth, spill }, null, 2),
      contentType: 'application/json',
    })
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-13-09 · the line-item grid on the deployed fleet -------------------------------
//
// The FINAL subtask of EXTR-13. Everything provable in jsdom (LineItemGrid.test.tsx,
// lineItems.test.ts, ExtractionReview.test.tsx) or over HTTP (EXTR13-API-01/02/03 in
// e2e/api/extractions.spec.ts) is deliberately NOT re-proven here. What is left is what only a
// browser against the deployed fleet can observe: the grid's real geometry at four widths, the
// live recompute, the row -> region highlight, and one Save that reaches the deployed store.
//
// THE NINE PINNED SPECS this story's sweep named, and where each stands. All nine were moved by
// the earlier subtasks; this row is the ledger, not a second edit:
//   EXTR11-E2E-02a (:3155)  UPDATED in 13-07 -- its `extraction-field-` sweep is bounded to the
//                           header vocabulary (`headerNamesA`), so a line cell cannot join it.
//   EXTR12-E2E-07 (:5307)   UPDATED in 13-07 -- same bound at the pane's 470px floor
//                           (`headerNamesB`); the grid sits BELOW the two-column grid and is not
//                           a third column, which EXTR13-LAYOUT-03 below asserts from the front.
//   EXTR12-E2E-01 (:3569)   UPDATED in 13-02/07 -- the per-field pill loop excludes line names.
//   EXTR11-E2E-04/04b (:2858) UPDATED in 13-02 -- the wire body's field SET is the widened
//                           twenty-three, listed literally as the only deployed oracle for it.
//   EXTR11-E2E-11 (:4611)   UPDATED in 13-02/07 per D-13; the artboard table is since deleted.
//   EXTR12-E2E-06 (:4998)   UPDATED in 13-02 -- the `extraction-marker-` sweep resolves BY NAME
//                           to the three settled fields, so a line marker cannot stand in.
//   EXTR11-E2E-02 (:3257)   DELIBERATELY UNCHANGED -- it measures the shell's two flex siblings
//                           tiling the body. The grid is a child of the fields pane's scrolling
//                           body and adds no pane, so the tiling claim is untouched.
//   EXTR11-E2E-10 (:3328)   DELIBERATELY UNCHANGED -- the pane's 470px floor is declared on
//                           PANE, not derived from its content, and the grid absorbs its own
//                           overflow in `line-item-scroll` (EXTR13-LAYOUT-01), so it cannot
//                           raise that floor.
//   EXTR11-E2E-02b (:3395)  DELIBERATELY UNCHANGED -- it asserts the pane body is the scroller
//                           and the header does not move. The grid adds rows INSIDE that body,
//                           which strengthens its own precondition rather than changing it.
//
// TWO GAPS this subtask does NOT close, named so they are not mistaken for coverage:
//   1. The EXTRACTION-FOUND-NOTHING path has no deployed subject: the mock keys its fixtures by
//      SHA-256 and `uniquePdfBytes()` mints a fresh hash per upload, so every deployed run
//      resolves to `mockDefaultResult`, which always carries four lines. This is a gap in how
//      the panel is REACHED, not in the panel. The panel itself is deployed-covered by
//      EXTR13-E2E-02 below, which removes every row to reach the same single branch.
//   2. `extraction-write-error` still has no deployed coverage (jsdom only) -- unchanged from
//      EXTR-12, and not this story's to close.

// The RENDERED roles only, deliberately one short of LINE_CELL_RE below: line_tax rides the
// wire but has no grid input, so widening this set would make gridValues read a control that
// does not exist.
const LINE_ROLE_NAMES = ['description', 'quantity', 'unit_price', 'line_total'] as const
type LineRoleName = (typeof LINE_ROLE_NAMES)[number]

// lineItems.ts's LINE_FIELD_RE, restated rather than imported: e2e/ compiles against no
// frontend source, and a spec that imported the parser would assert the parser against itself.
const LINE_CELL_RE = /^line_items\[([1-9][0-9]*)\]\.(description|quantity|unit_price|line_total|line_tax)$/

type WireLineCell = { name: string; value: string | null; region: ExtractionRegion | null }
type WireLine = { index: number; cells: Partial<Record<LineRoleName, WireLineCell>> }

/** The line block off the wire the SPA itself consumed, in the grid's own row order. */
function wireLines(detail: ExtractionDetail): WireLine[] {
  const byIndex = new Map<number, WireLine>()
  for (const f of detail.fields) {
    const m = LINE_CELL_RE.exec(f.name)
    if (m === null) continue
    const index = Number(m[1])
    let row = byIndex.get(index)
    if (row === undefined) {
      row = { index, cells: {} }
      byIndex.set(index, row)
    }
    row.cells[m[2] as LineRoleName] = { name: f.name, value: f.value, region: f.region }
  }
  return [...byIndex.values()].sort((a, b) => a.index - b.index)
}

function decimalOf(raw: string | null | undefined): number | null {
  if (raw === null || raw === undefined || raw.trim() === '') return null
  const n = Number(raw)
  return Number.isFinite(n) ? n : null
}

// rowArithmetic's rule (lineItems.ts), computed here from the wire's own numbers so the grid is
// checked against the document rather than against the module that draws it. LINE_TOLERANCE is
// 0.01 and the comparison is STRICTLY greater, so exactly 0.01 does not flag; the 1e-9 absorbs
// the binary float this arithmetic runs in, three orders below the 150.00 the mock disagrees by.
function wireRowState(row: WireLine): 'ok' | 'flagged' | 'unchecked' {
  const q = decimalOf(row.cells.quantity?.value)
  const p = decimalOf(row.cells.unit_price?.value)
  const t = decimalOf(row.cells.line_total?.value)
  if (q === null || p === null || t === null) return 'unchecked'
  return Math.abs(q * p - t) > 0.01 + 1e-9 ? 'flagged' : 'ok'
}

/** Every rendered cell value, by ordinal then role, as the grid holds it right now. */
async function gridValues(page: Page, rowCount: number): Promise<Record<LineRoleName, string>[]> {
  const out: Record<LineRoleName, string>[] = []
  for (let n = 1; n <= rowCount; n += 1) {
    const row = {} as Record<LineRoleName, string>
    for (const role of LINE_ROLE_NAMES) {
      row[role] = await page.getByTestId(`line-item-input-${n}-${role}`).inputValue()
    }
    out.push(row)
  }
  return out
}

/** The rendered ordinals of a `line-item-*` namespace, ascending. */
async function lineOrdinals(page: Page, prefix: string): Promise<number[]> {
  const ids = await page
    .locator(`[data-testid^="${prefix}"]`)
    .evaluateAll((els) => els.map((el) => (el as HTMLElement).dataset.testid ?? ''))
  return ids.map((id) => Number(id.slice(prefix.length))).sort((a, b) => a - b)
}

/**
 * One upload, one review screen, and the floors every claim below rests on.
 *
 * The floors are asserted rather than assumed: a fixture change that flattened the mock's line
 * block would otherwise leave every sweep in this section measuring nothing and passing.
 */
async function openLineGrid(page: Page, label: string): Promise<{ detail: ExtractionDetail; lines: WireLine[] }> {
  await extractOneDocument(page, label)
  const detail = await openExtractionReview(page)
  const lines = wireLines(detail)
  expect(
    lines.length,
    'this document delivered no line-item cell on the wire -- every grid claim below is vacuous',
  ).toBeGreaterThanOrEqual(2)

  await expect(page.getByTestId('line-item-grid'), 'the fields pane rendered no line-item grid').toBeVisible({
    timeout: 30_000,
  })
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    'the grid did not render one row per line the wire carried',
  ).toHaveCount(lines.length)
  return { detail, lines }
}

test('EXTR13-E2E-01 (Core AC 1-7): the deployed grid reads, flags, sums, selects, remaps, grows, shrinks and saves', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { detail, lines } = await openLineGrid(page, 'EXTR-13-09 journey')

  // -- 1. every cell the wire carried reaches its own input, by name ------------------------
  const present = lines.flatMap((row, i) =>
    LINE_ROLE_NAMES.filter((role) => row.cells[role] !== undefined).map((role) => ({
      n: i + 1,
      role,
      wire: row.cells[role] as WireLineCell,
    })),
  )
  const absent = lines.flatMap((row, i) =>
    LINE_ROLE_NAMES.filter((role) => row.cells[role] === undefined).map((role) => ({ n: i + 1, role })),
  )
  expect(present.length, 'the wire carried no line cell at all').toBeGreaterThanOrEqual(LINE_ROLE_NAMES.length)
  // The control needle for the empty-cell arm below: with nothing absent it asserts nothing.
  expect(
    absent.length,
    'every line on this document is complete, so the empty-cell arm below has no subject',
  ).toBeGreaterThanOrEqual(1)

  for (const c of present) {
    await expect(
      page.getByTestId(`line-item-input-${c.n}-${c.role}`),
      `${c.wire.name} did not reach row ${c.n}'s ${c.role} input`,
    ).toHaveValue(c.wire.value ?? '')
  }
  for (const c of absent) {
    await expect(
      page.getByTestId(`line-item-input-${c.n}-${c.role}`),
      `row ${c.n} has no ${c.role} on the wire, yet the grid put something in that cell`,
    ).toHaveValue('')
  }

  // AIR08-E2E's Core AC 7 leg, free on this document: rich_invoice.pdf carries no AIFAKE-LINES
  // marker, so mergeAILines leaves every row untouched -- no chip and no ambiguous pill.
  await expect(page.locator('[data-testid^="line-item-chip-"]'), 'an unsteered document renders a chip').toHaveCount(0)
  await expect(
    page.locator('[data-testid^="line-item-ambiguous-"]'),
    'an unsteered document renders an ambiguous pill',
  ).toHaveCount(0)

  // -- 2. the flag is per row, and lands only where the wire's own numbers disagree ----------
  const states = lines.map((row, i) => ({ n: i + 1, state: wireRowState(row) }))
  const flagged = states.filter((s) => s.state === 'flagged').map((s) => s.n)
  const settledRows = states.filter((s) => s.state === 'ok').map((s) => s.n)
  expect(flagged.length, "no row's own numbers disagree -- the flag claim is vacuous").toBeGreaterThanOrEqual(1)
  expect(
    settledRows.length,
    'every checkable row disagrees, so a grid that flagged all of them would pass -- there is no negative arm',
  ).toBeGreaterThanOrEqual(1)
  expect(
    await lineOrdinals(page, 'line-item-flag-'),
    'the flagged rows are not the rows whose own numbers disagree',
  ).toEqual([...flagged].sort((a, b) => a - b))

  // -- 3. the table-level statement carries BOTH numbers ------------------------------------
  for (const row of lines) {
    const raw = row.cells.line_total?.value
    if (raw === null || raw === undefined) continue
    expect(raw, `${raw} carries more than two decimals, so the rendered sum is not toFixed(2)`).toMatch(
      /^-?\d+(\.\d{1,2})?$/,
    )
  }
  const totals = lines.map((r) => decimalOf(r.cells.line_total?.value)).filter((n): n is number => n !== null)
  expect(totals.length, 'no line carries a parseable total -- there is no sum to state').toBeGreaterThanOrEqual(2)
  const printedRaw = detail.fields.find((f) => f.name === 'subtotal')?.value ?? null
  expect(printedRaw, 'this document prints no subtotal -- the disagreement below cannot be shown').not.toBeNull()
  expect(printedRaw as string, 'the printed subtotal carries more than two decimals').toMatch(/^-?\d+(\.\d{1,2})?$/)
  const summed = totals.reduce((a, b) => a + b, 0)
  const printed = decimalOf(printedRaw) as number
  expect(
    Math.abs(summed - printed),
    'the lines already agree with the printed subtotal, so the statement does not render at all',
  ).toBeGreaterThan(0.01)

  const sumLine = page.getByTestId('line-item-sum')
  await expect(sumLine, 'the lines disagree with the printed subtotal and the grid says nothing').toBeVisible()
  const sumText = (await sumLine.textContent()) ?? ''
  expect(sumText, `the statement does not carry the summed figure ${summed.toFixed(2)}`).toContain(summed.toFixed(2))
  expect(sumText, `the statement does not carry the printed figure ${printed.toFixed(2)}`).toContain(printed.toFixed(2))

  // -- 4. a line cell selects, and the document draws THAT cell's region ---------------------
  const located = present.find((c) => c.wire.region !== null)
  expect(located, 'no line cell carries a region -- the highlight claim is vacuous').toBeTruthy()
  const region = (located as { wire: WireLineCell }).wire.region as ExtractionRegion
  const cell = page.getByTestId(`line-item-cell-${located!.n}-${located!.role}`)
  await cell.click()
  const imageId = `extraction-page-image-${region.page}`
  await expect(page.getByTestId(imageId), "the selected cell's page must load").toBeVisible({ timeout: 60_000 })
  await expect(cell, 'the clicked line cell does not read as selected').toHaveAttribute('aria-current', 'true')
  const highlight = page.getByTestId('extraction-highlight')
  await expect(highlight, 'a selected line cell drew no highlight, or drew more than one').toHaveCount(1)
  // The identity claim, and AC-7's whole point: a line cell reaches the canvas over the SAME
  // channel a header cell does, so the box the document paints names the line field by name.
  await expect(highlight, 'the highlight names a field other than the line cell that was clicked').toHaveAttribute(
    'data-snip',
    located!.wire.name,
  )
  const highlightBox = await boxOf(page, 'extraction-highlight')
  const imageBox = await boxOf(page, imageId)
  expect(
    highlightBox.width * highlightBox.height,
    'the highlight has no area, so the containment below is vacuous',
  ).toBeGreaterThan(0)
  const inside = overlapOf(highlightBox, imageBox)
  expect(inside.width, 'the highlight is not horizontally inside the page it names').toBeGreaterThanOrEqual(
    highlightBox.width - RATIO_TOL_PX,
  )
  expect(inside.height, 'the highlight is not vertically inside the page it names').toBeGreaterThanOrEqual(
    highlightBox.height - RATIO_TOL_PX,
  )

  // -- 5. correcting the flagged row recomputes live, with no request ------------------------
  const flaggedN = flagged[0]
  const flaggedRow = lines[flaggedN - 1]
  const q = decimalOf(flaggedRow.cells.quantity?.value) as number
  const p = decimalOf(flaggedRow.cells.unit_price?.value) as number
  const settledTotal = (q * p).toFixed(2)
  expect(
    settledTotal,
    'the corrected total equals what row ' + flaggedN + ' already holds, so typing it moves nothing',
  ).not.toBe(flaggedRow.cells.line_total?.value)

  const lineWrites: string[] = []
  page.on('request', (r) => {
    if (r.method() !== 'POST') return
    if (new URL(r.url()).pathname.endsWith('/line-items')) lineWrites.push(r.url())
  })

  await page.getByTestId(`line-item-input-${flaggedN}-line_total`).fill(settledTotal)
  await expect(
    page.getByTestId(`line-item-flag-${flaggedN}`),
    `row ${flaggedN}'s numbers now agree and it still carries its flag`,
  ).toHaveCount(0)
  expect(
    await lineOrdinals(page, 'line-item-flag-'),
    'correcting one row moved another row’s flag',
  ).toEqual(flagged.filter((n) => n !== flaggedN))
  await expect(
    page.getByTestId(`line-item-marker-${flaggedN}-line_total`),
    'the corrected cell shows no changed marker',
  ).toBeVisible()
  expect(lineWrites, 'a live recompute posted to the server -- nothing reaches the store before Save').toEqual([])

  // -- 6. remapping a column moves the cells, and remapping back restores them ---------------
  const swappable = lines
    .map((row, i) => ({ n: i + 1, row }))
    .filter(
      ({ row }) =>
        row.cells.quantity !== undefined &&
        row.cells.unit_price !== undefined &&
        row.cells.quantity.value !== row.cells.unit_price.value,
    )
  expect(
    swappable.length,
    'no row carries two DIFFERENT values in the two columns being swapped -- a swap could not be observed',
  ).toBeGreaterThanOrEqual(1)

  const quantityColumn = page.getByTestId('line-item-role-quantity')
  await quantityColumn.selectOption('unit_price')
  for (const { n, row } of swappable) {
    await expect(
      page.getByTestId(`line-item-input-${n}-quantity`),
      `row ${n}'s quantity column did not take the unit price`,
    ).toHaveValue(row.cells.unit_price?.value ?? '')
    await expect(
      page.getByTestId(`line-item-input-${n}-unit_price`),
      `row ${n}'s unit-price column did not take the quantity`,
    ).toHaveValue(row.cells.quantity?.value ?? '')
  }
  // The selector always reads its OWN column's role, so the same choice repeats the swap.
  await quantityColumn.selectOption('unit_price')
  for (const { n, row } of swappable) {
    await expect(
      page.getByTestId(`line-item-input-${n}-quantity`),
      `row ${n}'s quantity was not restored by the reverse remap`,
    ).toHaveValue(row.cells.quantity?.value ?? '')
    await expect(
      page.getByTestId(`line-item-input-${n}-unit_price`),
      `row ${n}'s unit price was not restored by the reverse remap`,
    ).toHaveValue(row.cells.unit_price?.value ?? '')
  }

  // -- 7. add and remove, with the ordinals staying 1..N --------------------------------------
  const rowCount = lines.length
  await page.getByTestId('line-item-add').click()
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    'Add left the row count where it was',
  ).toHaveCount(rowCount + 1)
  await expect(
    page.getByTestId(`line-item-input-${rowCount + 1}-description`),
    'the appended row is not numbered N+1, or carries a value it was never given',
  ).toHaveValue('')
  await page.getByTestId(`line-item-remove-${rowCount + 1}`).click()
  await expect(page.locator('[data-testid^="line-item-row-"]'), 'Remove left the row count where it was').toHaveCount(
    rowCount,
  )
  expect(await lineOrdinals(page, 'line-item-row-'), 'the row ordinals are not 1..N with no gap').toEqual(
    Array.from({ length: rowCount }, (_, i) => i + 1),
  )

  // -- 8. one Save, one line POST, and the store answers -------------------------------------
  const save = page.getByTestId('extraction-save')
  await expect(save, 'a line-only edit left Save disabled').toBeEnabled()

  const posts: { status: number; body: Promise<Record<string, unknown> | null> }[] = []
  page.on('response', (r) => {
    if (r.request().method() !== 'POST') return
    if (!new URL(r.url()).pathname.endsWith('/line-items')) return
    posts.push({ status: r.status(), body: (r.json() as Promise<Record<string, unknown>>).catch(() => null) })
  })

  const [reread] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 120_000 },
    ),
    save.click(),
  ])
  expect(reread.status(), 'the post-save re-read failed').toBe(200)

  await expect
    .poll(() => posts.length, { message: 'the Save posted no line set', timeout: 15_000 })
    .toBe(1)
  expect(posts[0].status, 'the line set was refused').toBe(201)
  const posted = await posts[0].body
  expect(posted, 'the line-items 201 carried no body').not.toBeNull()
  const echoed = (posted as Record<string, unknown>).lines as Record<string, string | null>[]
  expect(echoed.length, 'the server wrote a different number of lines than the grid held').toBe(rowCount)
  expect(
    echoed.map((l) => l.line_total),
    'the total that was corrected is not in the set the server wrote',
  ).toContain(settledTotal)

  // The server's own values, read back. The line set lands on the `line_items` BLOCK row as one
  // canonical-JSON correction (writeLineItems, handlers_lineitems.go), and the reader projects that row
  // onto the per-cell `line_items[N].role` readings, which no correction ever names directly
  // (expandLineCorrection, internal/extraction/reader.go). The block row is where the write's
  // own record is checked, below; the cells it projects onto are what the reopen reads.
  const fresh = (await reread.json()) as ExtractionDetail
  const block = fresh.fields.find((f) => f.name === 'line_items')
  expect(block, 'the re-read carries no line_items block row -- the correction had nothing to land on').toBeTruthy()
  expect(block!.corrected, 'the Save reached the store and left no correction on the block row').not.toBeNull()
  expect(block!.corrected!.method, 'a line set is recorded as typed').toBe('typed')
  expect(
    block!.value,
    'the block row does not hold the set the server echoed at 201 -- the write and the read disagree',
  ).toBe(JSON.stringify(echoed))

  await expect(page.getByTestId('extraction-write-error'), 'the Save reported an error').toHaveCount(0)
  await expect(save, 'a committed Save left something still to save').toBeDisabled()

  // The reopen, and the defect it closes: a saved line correction snapped back to the
  // extractor's own reading on every reopen. The re-read JSON above proves the write happened,
  // never that a fresh mount shows it.
  //
  // NOT a reload. The screen has no URL of its own -- App.tsx renders it while
  // `view === 'extraction'` off a jobId in component state, and nothing puts that jobId in the
  // hash -- so a reload lands on the default view and re-requests no detail. Sidebar out,
  // entry control back in, which unmounts ExtractionReview and drops the draft it held.
  //
  // Nav idiom: invoice-surfaces.spec.ts's goToInvoices/openInvoiceRow. The row is taken by
  // count -- this entity was created for this run and the list is narrowed to it server-side --
  // so a second row fails here rather than opening some other invoice.
  await page.locator('aside.pf-sidebar nav.pf-nav-list').getByRole('button', { name: /Invoices/ }).click()
  await expect(page, 'inline nav to Invoices did not update the URL').toHaveURL(/\/invoices$/)
  await expect(page.getByTestId('invoices-list'), 'the sidebar nav did not land on the invoices list').toBeVisible({
    timeout: 60_000,
  })
  await expect(
    page.getByTestId('extraction-review'),
    'the review screen is still mounted, so what follows is not a reopen',
  ).toHaveCount(0)
  const invoiceRows = page.getByTestId('invoice-row')
  await expect(
    invoiceRows,
    'this entity was created for this run and holds exactly the one imported invoice',
  ).toHaveCount(1)
  await invoiceRows.click()
  await expect(page.getByTestId('invoice-detail'), 'the row did not open the live invoice detail').toBeVisible({
    timeout: 60_000,
  })

  // The same entry control every spec in this section opens the screen with.
  const reopened = await openExtractionReview(page)
  const savedCell = flaggedRow.cells.line_total as WireLineCell
  const reopenedCell = reopened.fields.find((f) => f.name === savedCell.name)
  expect(reopenedCell, `the reopened detail carries no ${savedCell.name} reading at all`).toBeTruthy()
  expect(
    reopenedCell!.value,
    `${savedCell.name} came back from a fresh read as the extractor's own reading ` +
      `(${savedCell.value}) instead of the saved ${settledTotal}`,
  ).toBe(settledTotal)
  await expect(page.getByTestId('line-item-grid'), 'the reopened page rendered no line-item grid').toBeVisible({
    timeout: 30_000,
  })
  await expect(
    page.getByTestId(`line-item-input-${flaggedN}-line_total`),
    `row ${flaggedN}'s line_total snapped back to the extractor's own reading ` +
      `(${flaggedRow.cells.line_total?.value}) on reopen, instead of keeping the saved ${settledTotal}`,
  ).toHaveValue(settledTotal)

  await testInfo.attach('extraction-line-grid-journey.json', {
    body: JSON.stringify(
      { lines: lines.length, flagged, settledRows, summed, printed, settledTotal, echoed },
      null,
      2,
    ),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR13-E2E-02 · the columns E2E-01 leaves untouched, and the empty panel -------------
//
// Its own upload, because E2E-01 ends on a Save and a reopen: an edit added after that point
// could not be restored, and one added before it would move the set that Save posts. Nothing
// here saves, so every gesture below is free to leave the draft where it lands.

test('EXTR13-E2E-02 (Core AC 2, 7, 8): every column selector, both untyped columns, and the empty panel', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { lines } = await openLineGrid(page, 'EXTR-13-13 columns and empty')
  const rowCount = lines.length

  // -- 1. all four column selectors render, each reading its own column ----------------------
  // E2E-01 drives `line-item-role-quantity` alone, so a grid that shipped one selector passes it.
  for (const role of LINE_ROLE_NAMES) {
    const select = page.getByTestId(`line-item-role-${role}`)
    await expect(select, `the ${role} column carries no role selector`).toBeVisible()
    await expect(select, `the ${role} selector does not read its own column`).toHaveValue(role)
    await expect(select, `the ${role} selector is not operable`).toBeEnabled()
  }

  // -- 2. each of the four is individually operable, and moves only its own pair -------------
  // Expectations are read off the grid immediately before each swap rather than off the wire,
  // so a later pair is measured against what the earlier ones actually left on screen.
  const pairs: [LineRoleName, LineRoleName][] = [
    ['description', 'quantity'],
    ['quantity', 'unit_price'],
    ['unit_price', 'line_total'],
    ['line_total', 'description'],
  ]
  for (const [driver, partner] of pairs) {
    const before = await gridValues(page, rowCount)
    expect(
      before.filter((row) => row[driver] !== row[partner]).length,
      `no row holds different values in ${driver} and ${partner} -- a swap could not be observed`,
    ).toBeGreaterThanOrEqual(1)

    await page.getByTestId(`line-item-role-${driver}`).selectOption(partner)
    const after = await gridValues(page, rowCount)
    for (let i = 0; i < before.length; i += 1) {
      const n = i + 1
      expect(after[i][driver], `row ${n}'s ${driver} column did not take the ${partner}`).toBe(before[i][partner])
      expect(after[i][partner], `row ${n}'s ${partner} column did not take the ${driver}`).toBe(before[i][driver])
      for (const other of LINE_ROLE_NAMES) {
        if (other === driver || other === partner) continue
        expect(after[i][other], `the ${driver} remap disturbed row ${n}'s ${other} column`).toBe(before[i][other])
      }
    }

    // The selector always reads its OWN column's role, so the same choice repeats the swap.
    await page.getByTestId(`line-item-role-${driver}`).selectOption(partner)
    expect(await gridValues(page, rowCount), `the ${driver} remap is not its own inverse`).toEqual(before)
  }

  // -- 3. the two columns E2E-01 never types into --------------------------------------------
  const settled = lines.map((row, i) => ({ n: i + 1, row })).filter(({ row }) => wireRowState(row) === 'ok')
  expect(
    settled.length,
    'no row on this document settles, so there is no row a keystroke can be shown to break',
  ).toBeGreaterThanOrEqual(1)
  const { n: settledN, row: settledRow } = settled[0]
  await expect(
    page.getByTestId(`line-item-flag-${settledN}`),
    `the BEFORE arm: row ${settledN} settles on the wire and must start unflagged`,
  ).toHaveCount(0)

  const typed = 'Rebar, 12mm, cut to length'
  expect(
    settledRow.cells.description?.value,
    'this row already holds the description about to be typed, so the edit moves nothing',
  ).not.toBe(typed)
  const descriptionInput = page.getByTestId(`line-item-input-${settledN}-description`)
  await descriptionInput.fill(typed)
  await descriptionInput.press('Tab')
  await expect(descriptionInput, 'the description column took no keystroke').toHaveValue(typed)
  await expect(
    page.getByTestId(`line-item-marker-${settledN}-description`),
    'the typed description shows no changed marker',
  ).toBeVisible()
  await expect(
    page.getByTestId(`line-item-flag-${settledN}`),
    'a description edit moved the row arithmetic',
  ).toHaveCount(0)

  // The auto-derive arm: a grid deriving line_total from quantity x unit price would rewrite
  // this row's total to match and never flag it.
  const q = decimalOf(settledRow.cells.quantity?.value) as number
  const documentTotal = settledRow.cells.line_total?.value as string
  const brokenPrice = ((decimalOf(settledRow.cells.unit_price?.value) as number) + 1000).toFixed(2)
  expect(
    Math.abs(q * Number(brokenPrice) - (decimalOf(documentTotal) as number)),
    `row ${settledN} still settles at a unit price of ${brokenPrice} -- the flag below would never appear`,
  ).toBeGreaterThan(0.01 + 1e-9)

  const priceInput = page.getByTestId(`line-item-input-${settledN}-unit_price`)
  await priceInput.fill(brokenPrice)
  await priceInput.press('Tab')
  await expect(priceInput, 'the unit-price column took no keystroke').toHaveValue(brokenPrice)
  await expect(
    page.getByTestId(`line-item-flag-${settledN}`),
    'the unit-price keystroke did not recompute the row flag',
  ).toBeVisible()
  await expect(
    page.getByTestId(`line-item-input-${settledN}-line_total`),
    'the grid derived the line total from quantity x unit price instead of keeping what the document read',
  ).toHaveValue(documentTotal)

  // -- 4. removing every line lands on the empty panel ----------------------------------------
  await expect(
    page.getByTestId('line-item-empty'),
    'the empty panel showed while the document lines were still on screen',
  ).toHaveCount(0)
  for (let left = rowCount; left > 0; left -= 1) {
    await page.getByTestId('line-item-remove-1').click()
    await expect(
      page.locator('[data-testid^="line-item-row-"]'),
      `removing the first of ${left} rows left the row count where it was`,
    ).toHaveCount(left - 1)
  }

  const empty = page.getByTestId('line-item-empty')
  await expect(empty, 'a grid holding no row renders no panel at all').toBeVisible()
  await expect(empty, 'the panel does not say the document carries no lines').toContainText(
    'We found no line items on this document.',
  )
  await expect(empty, 'the panel does not say what an empty grid costs').toContainText(
    'An invoice cannot be filed until it has at least one line, so add one here.',
  )
  await expect(
    page.getByTestId('line-item-grid').locator('table'),
    'an empty table survived the last removal',
  ).toHaveCount(0)
  await expect(page.getByTestId('line-item-sum'), 'a grid holding no line still states a sum').toHaveCount(0)

  // Not a dead end: the panel's own Add is the way back to a fileable invoice.
  await page.getByTestId('line-item-add').click()
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    'Add from the empty panel produced no row',
  ).toHaveCount(1)
  await expect(page.getByTestId('line-item-empty'), 'the panel outlived the row it asked for').toHaveCount(0)
  for (const role of LINE_ROLE_NAMES) {
    await expect(
      page.getByTestId(`line-item-input-1-${role}`),
      `the added row's ${role} is not a blank, editable cell`,
    ).toHaveValue('')
  }

  await testInfo.attach('extraction-line-columns-and-empty.json', {
    body: JSON.stringify({ rowCount, settledN, typed, brokenPrice, documentTotal }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- the four layout relationships -------------------------------------------------------
//
// Relationships, never dimensions (layout.ts's thesis): containment in a named parent,
// equality with a named sibling, the contained-overflow PAIR, and reachability across a
// measured extent. `assertFillsColumn` is deliberately not used for any of them -- it bounds
// the gaps from ABOVE only, so a scrollbox spilling its pane satisfies it.

// The fields pane's two children, in the artboard's order -- EXTR11-E2E-02b's own indices, and
// ExtractionFields.test.tsx pins that shape from the other side.
function fieldsPaneBody(page: Page): Locator {
  return page.getByTestId('extraction-fields').locator('> div').nth(1)
}

test('EXTR13-LAYOUT-01: the grid scrollbox stays inside the fields pane body, on both edges, at every width', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await openLineGrid(page, 'EXTR-13-09 contained')

  const paneBody = fieldsPaneBody(page)
  const scroll = page.getByTestId('line-item-scroll')

  const measured: { width: number; left: number; right: number; bodyScrollWidth: number; bodyClientWidth: number }[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first, WIDE_WIDTHS' own order.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [s, b] = await Promise.all([scroll.boundingBox(), paneBody.boundingBox()])
        const flow = await paneBody.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        return { s, b, flow }
      }, `line grid containment at ${width}px`)

      expect(m.s && m.b, `both the scrollbox and the pane body must render at ${width}px`).toBeTruthy()
      // Non-empty first: a rect collapsed to zero is inside anything and passes vacuously.
      expect(m.s!.width, `the scrollbox collapsed to zero width at ${width}px -- its edges are vacuous`).toBeGreaterThan(0)
      expect(m.b!.width, `the pane body collapsed to zero width at ${width}px`).toBeGreaterThan(0)

      // gaps()'s rule, with a FLOOR: a positive gap is slack, a negative one is a spill. Bounding
      // it from above would pass on the very defect this exists to catch.
      const g = gaps(m.s as Rect, m.b as Rect)
      expect(g.left, `the scrollbox starts ${(-g.left).toFixed(1)}px left of the pane body at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the scrollbox ends ${(-g.right).toFixed(1)}px right of the pane body at ${width}px`).toBeGreaterThanOrEqual(-1)

      // The other half of containment: the pane body declares `overflow-y: auto` (y ONLY), so a
      // grid whose overflow escaped its own scrollbox would have nowhere to hide.
      expect(
        m.flow.scrollWidth,
        `the pane body holds ${m.flow.scrollWidth}px of content in a ${m.flow.clientWidth}px box at ${width}px -- the grid's overflow escaped its scrollbox`,
      ).toBeLessThanOrEqual(m.flow.clientWidth + 1)

      measured.push({
        width,
        left: g.left,
        right: g.right,
        bodyScrollWidth: m.flow.scrollWidth,
        bodyClientWidth: m.flow.clientWidth,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([
    ...WIDE_WIDTHS,
  ])

  await testInfo.attach('extraction-line-grid-containment.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Forty is the story's own number (Core AC 9). Nothing on the deployed fleet PRODUCES forty
// lines -- the mock emits four and its fixtures are keyed by content hash -- so the grid is
// grown by the gesture a person would use.
const FORTY_LINES = 40

test('EXTR13-LAYOUT-02: forty lines overflow the scrollbox at 1280 and never the page', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { lines } = await openLineGrid(page, 'EXTR-13-09 forty')

  const add = page.getByTestId('line-item-add')
  for (let n = lines.length; n < FORTY_LINES; n += 1) await add.click()
  await expect(
    page.locator('[data-testid^="line-item-row-"]'),
    `the grid did not reach ${FORTY_LINES} rows`,
  ).toHaveCount(FORTY_LINES)
  await expect(page.getByTestId(`line-item-row-${FORTY_LINES}`), `row ${FORTY_LINES} did not render`).toBeVisible()

  const scroll = page.getByTestId('line-item-scroll')
  const readAt = async (width: number) => {
    await page.setViewportSize({ width, height: 1080 })
    const box = await settledRead(
      () => scroll.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth })),
      `forty-line overflow at ${width}px`,
    )
    // Core AC 9: the page itself never gains a horizontal scroll under forty lines.
    const pageScroll = await assertPageDoesNotScrollSideways(page, `forty lines at ${width}px`)
    return { width, box, pageScroll }
  }

  const entryViewport = page.viewportSize()
  // Widest first, the house order.
  const sweep: Awaited<ReturnType<typeof readAt>>[] = []
  try {
    sweep.push(await readAt(2560))
    sweep.push(await readAt(1280))
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }
  expect(sweep.map((s) => s.width), 'both arms must be measured, widest first').toEqual([2560, 1280])
  const [wide, narrow] = sweep

  expect(narrow.box.clientWidth, 'the scrollbox has no width at 1280px -- every claim here is vacuous').toBeGreaterThan(0)
  expect(wide.box.clientWidth, 'the scrollbox has no width at 2560px').toBeGreaterThan(0)

  // The PAIR is what proves containment rather than absence. Half one: at 1280 the grid really
  // does overflow, so there is something for the scrollbox to hold.
  expect(
    narrow.box.scrollWidth,
    `the grid fits its scrollbox at 1280px (${narrow.box.scrollWidth} in ${narrow.box.clientWidth}) -- there is no overflow to contain, so the page claim proves nothing`,
  ).toBeGreaterThan(narrow.box.clientWidth + 1)

  // Half two, the page never scrolling sideways, is asserted inside readAt at both arms.

  // The control arm. Without it a grid pinned to a constant width -- one that ALWAYS overflows,
  // at any viewport -- passes both halves above. At 2560 the same forty lines fit their
  // scrollbox, which places the overflow in the viewport rather than in the grid.
  expect(
    wide.box.scrollWidth,
    `the same forty lines still overflow at 2560px (${wide.box.scrollWidth} in ${wide.box.clientWidth}) -- the grid overflows by construction, not because the window is narrow`,
  ).toBeLessThanOrEqual(wide.box.clientWidth + 1)

  await testInfo.attach('extraction-line-grid-forty.json', {
    body: JSON.stringify({ rows: FORTY_LINES, wide, narrow }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR13-LAYOUT-03: the grid takes the same left and right edges as the header grid above it', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { detail } = await openLineGrid(page, 'EXTR-13-09 aligned')

  // The sibling this aligns to, anchored by what it CONTAINS rather than by position alone: a
  // wrong ancestor resolves zero header cells and reds here instead of measuring the wrong box.
  const headerNames = detail.fields.map((f) => f.name).filter((n) => !n.startsWith('line_items'))
  expect(headerNames.length, 'this document carries no header field -- there is no sibling to align to').toBeGreaterThan(0)
  const paneBody = fieldsPaneBody(page)
  const headerGrid = paneBody.locator('> div').first()
  await expect(
    headerGrid.locator('> [data-testid^="extraction-field-"]'),
    'the first pane-body child is not the header field grid',
  ).toHaveCount(headerNames.length)

  const grid = page.getByTestId('line-item-grid')

  const measured: { width: number; left: number; right: number; headerWidth: number }[] = []
  const entryViewport = page.viewportSize()
  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [g, h] = await Promise.all([grid.boundingBox(), headerGrid.boundingBox()])
        return { g, h }
      }, `line grid alignment at ${width}px`)

      expect(m.g && m.h, `both grids must render at ${width}px`).toBeTruthy()
      expect(m.g!.width, `the line grid collapsed to zero width at ${width}px`).toBeGreaterThan(0)
      expect(m.h!.width, `the header grid collapsed to zero width at ${width}px`).toBeGreaterThan(0)

      // EQUALITY, not containment: bounded from both sides, so a grid indented inside the column
      // and a grid spilling past it both red. This is what says the line block is a full-width
      // section under the two-column header grid and not a third column of it.
      const g = gaps(m.g as Rect, m.h as Rect)
      expect(
        Math.abs(g.left),
        `the line grid's left edge is ${g.left.toFixed(1)}px off the header grid's at ${width}px`,
      ).toBeLessThanOrEqual(1)
      expect(
        Math.abs(g.right),
        `the line grid's right edge is ${g.right.toFixed(1)}px off the header grid's at ${width}px`,
      ).toBeLessThanOrEqual(1)

      measured.push({ width, left: g.left, right: g.right, headerWidth: m.h!.width })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([
    ...WIDE_WIDTHS,
  ])

  await testInfo.attach('extraction-line-grid-alignment.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR13-LAYOUT-04: the rightmost column is reachable inside the scroll extent', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await openLineGrid(page, 'EXTR-13-09 reachable')

  const scroll = page.getByTestId('line-item-scroll')
  // The last column's control, and the one a person has to reach to drop a line.
  const rightmost = page.getByTestId('line-item-remove-1')

  const entryViewport = page.viewportSize()
  let before: { extent: number; client: number; outRight: number } | null = null
  let after: { scrollLeft: number; outRight: number; outLeft: number; width: number } | null = null
  try {
    // 1280, the narrow end of WIDE_WIDTHS: it is where a fixed-width table has an extent at all.
    await page.setViewportSize({ width: 1280, height: 1080 })

    const start = await settledRead(async () => {
      const flow = await scroll.evaluate((el) => ({
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
        scrollLeft: el.scrollLeft,
      }))
      const [box, control] = await Promise.all([scroll.boundingBox(), rightmost.boundingBox()])
      return { flow, box, control }
    }, 'line grid reach, unscrolled')

    expect(start.box && start.control, 'the scrollbox and its rightmost control must both render').toBeTruthy()
    expect(start.control!.width, 'the rightmost control has no width -- its edges are vacuous').toBeGreaterThan(0)

    // There IS an extent to cross. Without this the scroll below moves nothing and every claim
    // after it is satisfied by a grid that was never cut off.
    expect(
      start.flow.scrollWidth,
      `the grid already fits at 1280px (${start.flow.scrollWidth} in ${start.flow.clientWidth}) -- there is no extent to reach across`,
    ).toBeGreaterThan(start.flow.clientWidth + 1)

    // The needle: unscrolled, the rightmost control really is outside the visible box. A control
    // that was already fully visible would make the reachability claim below vacuous.
    const startOutRight = start.control!.x + start.control!.width - (start.box!.x + start.box!.width)
    expect(
      startOutRight,
      'the rightmost control is already fully visible before any scrolling, so reaching it proves nothing',
    ).toBeGreaterThan(1)
    before = { extent: start.flow.scrollWidth, client: start.flow.clientWidth, outRight: startOutRight }

    await scroll.evaluate((el) => {
      el.scrollLeft = el.scrollWidth
    })

    const end = await settledRead(async () => {
      const scrollLeft = await scroll.evaluate((el) => el.scrollLeft)
      const [box, control] = await Promise.all([scroll.boundingBox(), rightmost.boundingBox()])
      return { scrollLeft, box, control }
    }, 'line grid reach, scrolled to the end')

    expect(end.box && end.control, 'the scrollbox and its rightmost control must still render after scrolling').toBeTruthy()
    expect(end.scrollLeft, 'the scrollbox did not move -- it is not the scroller').toBeGreaterThan(0)

    // Reachability, on BOTH edges: at the end of its own extent the rightmost control is inside
    // the visible box. A grid clipped past its scroll extent reds on the right; one pushed off
    // the other way reds on the left.
    const outRight = end.control!.x + end.control!.width - (end.box!.x + end.box!.width)
    const outLeft = end.box!.x - end.control!.x
    expect(
      outRight,
      `the rightmost control is still ${outRight.toFixed(1)}px past the visible box at the end of the scroll extent`,
    ).toBeLessThanOrEqual(1)
    expect(
      outLeft,
      `the rightmost control sits ${outLeft.toFixed(1)}px left of the visible box at the end of the scroll extent`,
    ).toBeLessThanOrEqual(1)
    expect(end.control!.width, 'the rightmost control collapsed while being reached').toBeGreaterThan(0)
    after = { scrollLeft: end.scrollLeft, outRight, outLeft, width: end.control!.width }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  await testInfo.attach('extraction-line-grid-reach.json', {
    body: JSON.stringify({ before, after }, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-15-13 (task-857) · AC-1: the spreadsheet flow, untouched -----------------------
//
// EXTR-15-09 rewrote 31 copy sites behind a `unit` branch. Its hardest constraint is the one
// no census can state: the SPREADSHEET arm still renders on the deployed build, byte for byte.
// reviewCopy.census.test.ts pins BOTH literals of every site in SOURCE; only a browser can say
// which branch a real CSV run takes.
//
// DELIBERATELY ABOVE the EXTR-15 deployed-proof marker below. That span requires every
// `buffer:` argument inside it to be a fresh-per-call fixture helper, because a repeated
// document collides on the permanent per-document enqueue key and settles on a previous run's
// job. A CSV import enqueues no extraction at all, so the rule has nothing to protect here --
// the span's own header gives that same reason for the file's other Buffer.from(...) probes.
//
// TWO imports, because the three literals never share a screen. `ROWS READ` (census R1/R2) is
// the batch header and `Row` (U4) is the Unreadable tab's grid header, both on the batch
// surface; `Rows stored` (B5) is a RejectedFile tile, which renders only when NO batch in the
// run reached 'completed' (reviewShellStateAll, lib/reviewBatch.ts). A header-only file is the
// one fixture that reaches it.
//
// Each literal is asserted WITH its document twin's absence. Presence alone still passes on a
// screen rendering both branches, which is what a half-landed sweep looks like.

/**
 * The screen's rendered text, for byte-exact copy claims.
 *
 * NOT getByText(string), which is case-INSENSITIVE substring matching: it cannot state
 * "byte-identical", and it reads the tab label `Already imported (1)` as the tile value
 * `already imported`. innerText, never textContent -- textContent concatenates across element
 * boundaries and would manufacture substrings that nothing renders.
 */
async function screenText(page: Page): Promise<string> {
  return (await page.locator('body').innerText()).replace(/\u00a0/g, ' ')
}

// R1/R2's two arms minus the ${...} each interpolates, copied from the census table
// (frontend/app/src/lib/reviewCopy.census.test.ts) rather than retyped off the component.
const SPREADSHEET_READ_LINE = 'ROWS READ · SERVER VERDICT · RULE SET '
const DOCUMENT_READ_LINE = 'DOCUMENTS READ · SERVER VERDICT · RULE SET '

test('EXTR15-E2E-05 (AC-1): a spreadsheet run still reads ROWS READ, Rows stored and Row', async ({ page }) => {
  // Two full CSV imports through the wizard on a fleet that may be cold. Neither enqueues an
  // extraction, so this is the cheapest of the EXTR-15 deployed cases.
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-15-13 spreadsheet ${Date.now()}`, tin: freshTin() })

  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)

  // --- import 1: the mixed fixture, whose rows quarantine structurally (E2E-04's own flow) --
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'extr15-sweep.csv', mimeType: 'text/csv', buffer: Buffer.from(buildMixedCsv(), 'utf8') })

  const sweepPreview = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await sweepPreview

  // subtotal has no ALIAS entry, so auto-recognize never places it -- E2E-04's own reasoning.
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const sweepImport = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: /^Import \d+ rows$/ }).click()
  await sweepImport

  // (a) the batch header's counted noun (R1/R2), and the tab label's (R3).
  const unreadableTab = page.getByRole('button', { name: /^Unreadable rows \(\d+\)$/ })
  await expect(unreadableTab, "the mixed fixture's structural failures must open this tab").toBeVisible({
    timeout: 60_000,
  })
  await expect(
    page.getByRole('button', { name: /^Quarantined documents \(/ }),
    'the document tab label reached a spreadsheet run',
  ).toHaveCount(0)

  const batchScreen = await screenText(page)
  expect(batchScreen, 'the batch header must keep the spreadsheet noun').toContain(SPREADSHEET_READ_LINE)
  expect(batchScreen, 'the document branch reached a spreadsheet run').not.toContain(DOCUMENT_READ_LINE)

  // (b) the Unreadable tab's grid header (U4). Resolved from a SIBLING cell rather than
  // searched page-wide: an exact 'Row' is three characters and would match a cell elsewhere.
  // The chain is self-checked by what the resolved row must also contain (EXTR09-E2E-04's idiom).
  await unreadableTab.click()
  const whyCell = page.getByText('Why it could not be read', { exact: true })
  await expect(whyCell, "the unreadable grid's last header cell").toHaveCount(1)
  const headerRow = whyCell.locator('xpath=..')
  await expect(
    headerRow.getByText('File', { exact: true }),
    'the resolved element is the header row, not the card around it',
  ).toHaveCount(1)
  await expect(headerRow.getByText('Row', { exact: true }), 'the row-number column must still be headed Row').toHaveCount(1)
  await expect(
    headerRow.getByText('—', { exact: true }),
    "the document arm's em-dash header reached a spreadsheet run",
  ).toHaveCount(0)

  // --- import 2: a header-only file, the one fixture reaching RejectedFile (INVCR-E2E-4's) --
  await page.getByRole('button', { name: 'Finish · go to invoices' }).click()
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.locator('input[type="file"]#pf-import-file').setInputFiles({
    name: 'extr15-header-only.csv',
    mimeType: 'text/csv',
    buffer: Buffer.from(buildHeaderOnlyCsv(), 'utf8'),
  })

  const rejectedPreview = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await rejectedPreview

  // Import 1 saved this same PERF_HEADER's mapping; import 2 restores it instead of
  // re-mapping by hand.
  await expect(
    page.getByTestId('map-column').filter({ has: page.locator('div.mono', { hasText: /^Invoice No$/ }) }).getByTestId('map-restored-badge'),
  ).toHaveCount(1)

  const rejectedImport = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Import 0 rows' }).click()
  await rejectedImport

  // (c) the RejectedFile tiles (B5/B6) and the sentence above them (B4).
  await expect(page.getByText('Nothing was imported', { exact: true }), "§7.5's rejected-file surface").toBeVisible({
    timeout: 60_000,
  })
  const rejectedScreen = await screenText(page)
  expect(rejectedScreen, 'the stored tile must keep the spreadsheet noun').toContain('Rows stored')
  expect(rejectedScreen, "B5's document arm reached a spreadsheet run").not.toContain('Documents stored')
  expect(rejectedScreen, 'the quarantined tile must keep the spreadsheet noun').toContain('Rows quarantined')
  expect(rejectedScreen, "B6's document arm reached a spreadsheet run").not.toContain('Documents quarantined')
  expect(rejectedScreen, "B4's spreadsheet arm").toContain(
    'it held no data rows — a spreadsheet with only a header row, for example.',
  )
  expect(rejectedScreen, "B4's document arm reached a spreadsheet run").not.toContain(
    'nothing invoice-shaped could be found in it',
  )

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// Above the EXTR-15 marker: that span requires fresh bytes per upload, and all three imports
// here must share one file so the saved mapping keys on the same header.
test('EXTR37-E2E-01: the second import of a file for the same client opens mapped, and opens unmapped without the lookup', async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `Zz EXTR-37 restore ${Date.now()}`, tin: freshTin() })

  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)

  // One file for all three imports: the saved mapping keys on the decoded header.
  const csv = buildSingleInvoiceCsv(`INV-E2E-EXTR37-${Date.now()}`)

  const lookupPredicate = (r: Response) =>
    r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/saved-mapping')
  const notice = page.getByTestId('map-restored-notice')
  const badges = page.getByTestId('map-restored-badge')
  const invoiceNoColumn = page.getByTestId('map-column').filter({ has: page.locator('div.mono', { hasText: /^Invoice No$/ }) })
  const chip = page.getByRole('button', { name: 'invoice_number' })
  const backToAuto = notice.getByRole('button', { name: 'Use automatic suggestions' })
  const importBtn = page.getByRole('button', { name: /^Import \d+ rows$/ })
  const blockedBtn = page.getByRole('button', { name: 'Map invoice number to continue' })

  // --- import 1: nothing saved yet for this client -- the lookup answers null -------------
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'extr37-1.csv', mimeType: 'text/csv', buffer: Buffer.from(csv, 'utf8') })

  const preview1 = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  const lookup1 = page.waitForResponse(lookupPredicate, { timeout: 60_000 })
  await page.getByRole('button', { name: 'Read columns' }).click()

  const documentId1 = ((await (await preview1).json()) as { document_id?: string }).document_id
  const look1 = await lookup1
  expect(look1.status(), 'the saved-mapping lookup must answer 200').toBe(200)
  const look1Url = new URL(look1.url())
  expect(look1Url.searchParams.get('entity_id')).toBe(entity.id)
  expect(look1Url.searchParams.get('document_id')).toBe(documentId1)
  expect((await look1.json()).saved_mapping, "import 1 is this client's first import -- nothing saved yet").toBeNull()

  await expect(chip, 'the Map step must settle before any placement').toBeVisible()
  await expect(notice).toHaveCount(0)
  await expect(badges).toHaveCount(0)

  // --- import 1 lands: hand-placed, saved because remember_mapping defaults true ----------
  await page.getByRole('button', { name: 'invoice_number' }).click()
  await page.getByText('Invoice No', { exact: true }).click()
  await page.getByRole('button', { name: 'subtotal' }).click()
  await page.getByText('Subtotal', { exact: true }).click()

  const import1Req = page.waitForRequest(
    (r) => r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  const import1Resp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await importBtn.click()
  expect(requestBody(await import1Req), 'an untouched-vs-touched run is only provable if remember_mapping is on the wire').toMatch(
    /name="remember_mapping"\r\n\r\ntrue\r\n/,
  )
  expect((await import1Resp).status()).toBe(201)
  await expect(page.getByTestId('invoice-detail'), 'N=1 routes to the real detail').toBeVisible({ timeout: 30_000 })

  // --- import 2: the identical bytes -- the lookup must answer the saved mapping ----------
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'extr37-2.csv', mimeType: 'text/csv', buffer: Buffer.from(csv, 'utf8') })

  const preview2 = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  const lookup2 = page.waitForResponse(lookupPredicate, { timeout: 60_000 })
  await page.getByRole('button', { name: 'Read columns' }).click()
  await preview2

  const look2 = await lookup2
  expect(look2.status()).toBe(200)
  const saved = ((await look2.json()) as { saved_mapping: { mapping: Record<string, string>; saved_at: string } | null }).saved_mapping
  expect(saved, 'import 2 must restore what import 1 saved').not.toBeNull()
  expect(saved!.mapping.invoice_number).toBe('Invoice No')
  expect(saved!.mapping.subtotal).toBe('Subtotal')

  await expect(notice).toBeVisible()
  await expect(notice).toContainText("Mapping restored from this client's earlier import")
  await expect(backToAuto).toBeVisible()
  await expect(backToAuto).toBeEnabled()

  await expect(invoiceNoColumn.getByTestId('map-restored-badge')).toHaveCount(1)
  await expect(invoiceNoColumn.getByTestId('map-restored-badge')).toHaveText('RESTORED')
  // Every restored field carries the badge -- 7 auto-aliased plus the 2 hand-placed above.
  await expect(badges).toHaveCount(Object.keys(saved!.mapping).length)
  await expect(chip, 'a restored field is no longer an unplaced palette chip').toHaveCount(0)
  await expect(blockedBtn).toHaveCount(0)
  await expect(importBtn).toBeEnabled()

  // --- EXTR37-LAYOUT-01: containment only, at every WIDE_WIDTHS entry, before import 2 lands
  {
    const entryViewport = page.viewportSize()
    const measured: { width: number; columns: number }[] = []
    try {
      for (const width of WIDE_WIDTHS) {
        await page.setViewportSize({ width, height: 1080 })
        await expect(notice).toBeVisible()

        const badgedColumns = page.getByTestId('map-column').filter({ has: page.getByTestId('map-restored-badge') })
        // count() first: boundingBox() on a zero-match locator would wait out the whole test.
        const n = await badgedColumns.count()
        expect(n, `every restored column must carry its badge at ${width}px`).toBe(Object.keys(saved!.mapping).length)

        for (let i = 0; i < n; i++) {
          const col = badgedColumns.nth(i)
          const badge = col.getByTestId('map-restored-badge')
          const tag = badge.locator('xpath=..')
          const field = badge.locator('xpath=preceding-sibling::span[1]')
          const glyph = badge.locator('xpath=following-sibling::span[1]')

          const boxes = await settledRead(async () => {
            const [colBox, tagBox, badgeBox, fieldBox, glyphBox] = await Promise.all([
              col.boundingBox(),
              tag.boundingBox(),
              badge.boundingBox(),
              field.boundingBox(),
              glyph.boundingBox(),
            ])
            return { colBox, tagBox, badgeBox, fieldBox, glyphBox }
          }, `restored column ${i} geometry at ${width}px`)
          expect(
            boxes.colBox && boxes.tagBox && boxes.badgeBox && boxes.fieldBox && boxes.glyphBox,
            `column ${i}'s tag, badge, field name and glyph must all render at ${width}px`,
          ).toBeTruthy()

          const tagInCol = gaps(boxes.tagBox as Rect, boxes.colBox as Rect)
          const badgeInTag = gaps(boxes.badgeBox as Rect, boxes.tagBox as Rect)
          expect(Math.min(tagInCol.left, tagInCol.right), `column ${i}'s tag must stay inside its column at ${width}px`).toBeGreaterThanOrEqual(-0.5)
          expect(
            Math.min(badgeInTag.left, badgeInTag.right),
            `column ${i}'s badge must stay inside its tag at ${width}px`,
          ).toBeGreaterThanOrEqual(-0.5)
          expect(
            rectsOverlap(boxes.badgeBox as Rect, boxes.fieldBox as Rect),
            `column ${i}'s badge must not sit over its field name at ${width}px`,
          ).toBe(false)
          expect(
            rectsOverlap(boxes.badgeBox as Rect, boxes.glyphBox as Rect),
            `column ${i}'s badge must not sit over its unmap glyph at ${width}px`,
          ).toBe(false)
        }

        const noticeBoxes = await settledRead(async () => {
          const [buttonBox, noticeBox, cardBox] = await Promise.all([
            backToAuto.boundingBox(),
            notice.boundingBox(),
            notice.locator('xpath=..').boundingBox(),
          ])
          return { buttonBox, noticeBox, cardBox }
        }, `restored notice geometry at ${width}px`)
        expect(
          noticeBoxes.buttonBox && noticeBoxes.noticeBox && noticeBoxes.cardBox,
          `the notice, its button and its card must all render at ${width}px`,
        ).toBeTruthy()
        const buttonInNotice = gaps(noticeBoxes.buttonBox as Rect, noticeBoxes.noticeBox as Rect)
        const noticeInCard = gaps(noticeBoxes.noticeBox as Rect, noticeBoxes.cardBox as Rect)
        expect(
          Math.min(buttonInNotice.left, buttonInNotice.right),
          `the return-to-automatic button must stay inside the notice at ${width}px`,
        ).toBeGreaterThanOrEqual(-0.5)
        expect(
          Math.min(noticeInCard.left, noticeInCard.right),
          `the notice must stay inside its card at ${width}px`,
        ).toBeGreaterThanOrEqual(-0.5)

        measured.push({ width, columns: n })
      }
    } finally {
      if (entryViewport) await page.setViewportSize(entryViewport)
    }
    expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])
    await testInfo.attach('extr37-layout.json', { body: JSON.stringify(measured, null, 2), contentType: 'application/json' })
  }

  // --- import 2 submitted untouched: remember_mapping=false, and 0 new invoices ------------
  const import2Req = page.waitForRequest(
    (r) => r.method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  const import2Resp = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports'),
    { timeout: 60_000 },
  )
  await importBtn.click()
  expect(requestBody(await import2Req), 'an untouched restore must post remember_mapping=false').toMatch(/name="remember_mapping"\r\n\r\nfalse\r\n/)
  expect((await import2Resp).status()).toBe(201)
  // Import 2 repeats import 1's invoice number, so it creates 0 invoices and routes to the
  // batch surface, not a second invoice detail.
  await expect(page.getByRole('button', { name: /^Invoices \(\d+\)$/ })).toBeVisible({ timeout: 60_000 })

  // --- import 3, under an intercepted lookup: the Map step must return to today's seed ----
  await page.getByRole('button', { name: 'Finish · go to invoices' }).click()
  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'extr37-3.csv', mimeType: 'text/csv', buffer: Buffer.from(csv, 'utf8') })

  const LOOKUP_GLOB = '**/api/invoice/v1/imports/saved-mapping*'
  let hits = 0
  const realBodies: { saved_mapping: { mapping: Record<string, string> } | null }[] = []
  await page.route(LOOKUP_GLOB, async (route) => {
    hits += 1
    const real = await route.fetch()
    realBodies.push(await real.json())
    // The real headers keep CORS; the two body headers would describe the real body, not this
    // one. An abort or a 5xx would log a console error and trip collectErrors below.
    const headers = real.headers()
    delete headers['content-length']
    delete headers['content-encoding']
    await route.fulfill({ status: real.status(), headers, json: { saved_mapping: null } })
  })
  // The saved row also reaches the screen through suggest-mapping, which does its own
  // server-side lookup -- intercepting only the route above no longer isolates it.
  const SUGGEST_GLOB = '**/api/invoice/v1/imports/suggest-mapping'
  let suggestHits = 0
  await page.route(SUGGEST_GLOB, async (route) => {
    suggestHits += 1
    const real = await route.fetch()
    const body = await real.json()
    const headers = real.headers()
    delete headers['content-length']
    delete headers['content-encoding']
    await route.fulfill({ status: real.status(), headers, json: { ...body, source: 'none', mapping: {} } })
  })

  const preview3 = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/preview'),
    { timeout: 60_000 },
  )
  await page.getByRole('button', { name: 'Read columns' }).click()
  await preview3
  await expect(chip).toBeVisible()

  await expect.poll(() => hits, { message: 'the intercepted lookup must fire exactly once' }).toBe(1)
  // The row still exists -- only this test's answer to the SPA differs from the real one.
  expect(realBodies[0]?.saved_mapping?.mapping.invoice_number, 'control: the real lookup still returns the saved row').toBe('Invoice No')
  await expect
    .poll(() => suggestHits, { message: 'the intercepted suggest-mapping must fire exactly once -- its own server-side lookup is the second route to the saved row' })
    .toBe(1)

  await expect(notice).toHaveCount(0)
  await expect(badges).toHaveCount(0)
  await expect(blockedBtn).toBeVisible()

  await page.unroute(LOOKUP_GLOB)
  await page.unroute(SUGGEST_GLOB)
  // Import 3 is never submitted -- its only purpose is proving the dependency on the lookup.

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// === EXTR-15 · the deployed proof: the terminal states and the hand-off =================
//
// GATE-ONLY, every test from here to the EXTR-18-07 marker below. Topology specs run only on
// the deploy gate and this PR's environment does not exist while the PR is a draft, so none
// of them has a local oracle beyond `typecheck` + `playwright test --list`.
//
// deployedProofGuards.test.ts scans exactly this span -- marker to marker -- and requires
// every `buffer:` argument inside it to be one of the fresh-per-call fixture helpers. The
// EXTR-18-07 block below carries the same rule under a narrower allowlist; this span is
// separate because two of its documents are DOCX, which no unique*PdfBytes() helper mints.
//
// --- EXTR-15-07 (task-833) · the hand-off row's geometry --------------------------------
//
// AC-10, authored in Mode A and UNEXECUTED here: topology specs run only on the deploy
// gate, and this PR's environment does not exist until it leaves draft. Subtask 13 is what
// runs it. The local oracle at authoring time was `pnpm --filter @invoice-os/e2e typecheck`
// plus `playwright test --list`.
//
// Placed ABOVE the EXTR-18-07 block deliberately: deployedProofGuards.test.ts scans that
// block from its marker to EOF and requires every `buffer:` argument in it to be a
// `unique*PdfBytes()` call. This test's fixture is uniqueGarbageBytes() — fresh per call for
// the same enqueue-key reason, but not a PDF and not that helper family.
//
// Reached by a run whose ONLY file dead-letters: routeAfterRun answers `none`, applyRoute
// calls markRunFailed, and CreateFlow renders the failures card on the 'documents' step.
// A single-file run removes EXTR10-E2E-02's race entirely -- 'failed' is terminal, so the
// card cannot be routed out from under the sweep and nothing needs holding open.
//
// Containment is measured with overlapOf (layout.ts), which returns a Rect. The
// identity `overlapOf(inner, outer) === inner` is the only expression of "inner is wholly
// inside outer"; rectsOverlap (layout.ts) returns a boolean and is true for a row
// hanging half out of its card, so it cannot state this at all.
//
// NO ASSERTION STATES A PIXEL WIDTH. A width assertion passes on the very bug it should
// catch (a row that fits at 2560 and overflows at 1280 has the same width at both). What
// is asserted instead is a RELATIONSHIP that must hold at every width.

test('EXTR15-E2E-01 (AC-10): the hand-off row sits inside its card, and its gutter holds at every width', async ({
  page,
}, testInfo) => {
  // Upload + enqueue + River's attempt^4s backoff to dead-letter, four widths, then AC-5's
  // hand-off journey (a filing plus an invoice-detail load) on the far side of all of it.
  test.setTimeout(420_000)
  const errors = collectErrors(page)

  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `EXTR-15-07 handoff ${Date.now()}`, tin: freshTin() })

  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()

  // Registered before the pick, same reason as EXTR09-E2E-01's own waiter: the upload can
  // resolve before an awaited setInputFiles returns. AC-5 below compares the filed invoice's
  // source document against THIS id.
  const documentPost = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/submission/v1/documents'),
    { timeout: 120_000 },
  )
  // *.pdf bytes that are not a PDF: pdfium refuses it on all three attempts and the worker
  // dead-letters deterministically (uniqueGarbageBytes' own doc comment).
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: 'handoff-dead-letter.pdf', mimeType: 'application/pdf', buffer: uniqueGarbageBytes() })
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  const handOffDocumentId = ((await (await documentPost).json()) as { document_id?: string }).document_id
  expect(handOffDocumentId, 'the refused document must still be stored').toMatch(/^[0-9a-fA-F-]{36}$/)

  const card = page.getByTestId('document-failures-card')
  await expect(card, 'an all-failed document run must land on the failures card').toBeVisible({ timeout: 180_000 })

  const rows = page.getByTestId('document-failure-row')
  // Non-empty FIRST: an empty locator satisfies every containment check below vacuously.
  await expect(rows, 'the failure must render as its own row').toHaveCount(1)
  await expect(rows.first(), 'the row must name why the document was refused').toContainText(DEAD_LETTER_NEEDLE)

  const button = rows.first().getByRole('button', { name: 'Enter it by hand' })
  await expect(button, 'a stored document must offer the hand-off').toHaveCount(1)
  const reason = rows.first().locator('span').filter({ hasText: DEAD_LETTER_NEEDLE }).first()
  await expect(reason, 'the reason sentence must be measurable').toHaveCount(1)

  type Sample = { width: number; clearance: number; rowWidth: number }
  const samples: Sample[] = []
  const entryViewport = page.viewportSize()

  try {
    // Widest first — WIDE_WIDTHS' own order (layout.ts): a cap strands only what the
    // window gives it room to strand.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const read = async () => {
        const [cardBox, rowBox, reasonBox, buttonBox, rowEdges] = await Promise.all([
          card.boundingBox(),
          rows.first().boundingBox(),
          reason.boundingBox(),
          button.boundingBox(),
          rows.first().evaluate(edgesOf),
        ])
        return { cardBox, rowBox, reasonBox, buttonBox, rowEdges }
      }
      const m = await settledRead(read, `hand-off row at ${width}px`)
      expect(
        m.cardBox && m.rowBox && m.reasonBox && m.buttonBox,
        `card, row, reason and button must all render at ${width}px`,
      ).toBeTruthy()

      // (a) the row is CONTAINED by the card — the intersection is the row's own rect.
      expect(
        sameRect(overlapOf(m.rowBox!, m.cardBox!), m.rowBox!),
        `the row must sit wholly inside the card at ${width}px (row ${JSON.stringify(m.rowBox)}, card ${JSON.stringify(m.cardBox)})`,
      ).toBe(true)

      // (b) the reason and the control are both contained by their row, same identity.
      expect(
        sameRect(overlapOf(m.reasonBox!, m.rowBox!), m.reasonBox!),
        `the reason must sit wholly inside its row at ${width}px (reason ${JSON.stringify(m.reasonBox)}, row ${JSON.stringify(m.rowBox)})`,
      ).toBe(true)
      expect(
        sameRect(overlapOf(m.buttonBox!, m.rowBox!), m.buttonBox!),
        `the control must sit wholly inside its row at ${width}px (button ${JSON.stringify(m.buttonBox)}, row ${JSON.stringify(m.rowBox)})`,
      ).toBe(true)

      // (c) the gutter between the control's right edge and the row's right CONTENT edge
      // (edgesOf subtracts the row's own border and padding, so this is the gap the
      // recipe's `padding: '10px 14px'` declares, not the border box).
      samples.push({
        width,
        clearance: m.rowEdges.right - (m.buttonBox!.x + m.buttonBox!.width),
        rowWidth: m.rowBox!.width,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(
    samples.map((s) => s.width),
    'every WIDE_WIDTHS entry must be measured, widest first',
  ).toEqual([...WIDE_WIDTHS])

  // The gutter is a RELATIONSHIP, compared against this run's own widest reading rather
  // than a literal. A row that fits at 2560 and overflows at 1280 moves this number;
  // a row whose width merely changes with the viewport does not.
  const widest = samples[0].clearance
  for (const s of samples) {
    expect(
      Math.abs(s.clearance - widest),
      `the control's clearance from the row's right content edge must not change with the viewport (${s.width}px: ${s.clearance}, 2560px: ${widest})`,
    ).toBeLessThanOrEqual(1)
    expect(s.clearance, `the control must not overflow the row's right content edge at ${s.width}px`).toBeGreaterThanOrEqual(-0.5)
  }

  // --- AC-5 (EXTR-15-12): the single-document hand-off, end to end -----------------------
  //
  // The geometry above proved the control is THERE. This walks it: enabled under a persona
  // whose entity is resolved, then the manual form, then a real filing, then the document
  // reappearing on the invoice's own detail screen. Appended to this test rather than given
  // its own so the ~3-minute dead-letter wait is paid once.
  await expect(button, 'a resolved entity must arm the hand-off, not merely render it').toBeEnabled()
  await button.click()

  const handOffNumber = `EXTR15-HO-${Date.now()}`
  const invoiceId = await fileHandOffDraft(page, handOffNumber)

  // The affirmation is the server's own row, the same one every other filing path lands on.
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(handOffNumber)

  // Equality against the file that was uploaded, never "a card is present": the card renders
  // for an invoice with NO document too, and says so.
  const sourceCard = page.getByTestId('source-document-card')
  await expect(sourceCard, "the hand-off's document must reach the invoice it produced").toContainText(
    'handoff-dead-letter.pdf',
  )
  await expect(page.getByTestId('view-source-document'), 'the stored file must be openable from here').toHaveCount(1)
  await expect(
    page.getByTestId('why-no-source-document'),
    'the card took its no-document arm -- source_document_id never crossed the create wire',
  ).toHaveCount(0)

  // And the wire behind it, read with this run's own token: the card renders a filename, and
  // two documents in one tenant can share one.
  const source = await readSourceDocument(token, invoiceId)
  expect(source.document, 'the source-document read returned no document for a hand-off invoice').not.toBeNull()
  expect(source.document!.id, "the invoice names a document that is not the one that failed").toBe(handOffDocumentId)

  await testInfo.attach('handoff-row-geometry.json', {
    body: JSON.stringify(samples, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-15-12 (task-836) · the pipeline, proved on the deployed build -----------------
//
// Three claims EXTR-15 makes that have no honest unit oracle, and can only be settled here:
//   1. the DEPLOYED sidecar reads a real DOCX -- the Go golden was recorded through a LOCAL
//      docling, and this project has already been burned by that once (a local canary read
//      55 OCR tokens where Railway read zero);
//   2. a hand-off from a MULTI-document run attaches the row's own document -- every other
//      guard on this is a mocked fixture, and a mock cannot catch an id-mapping defect
//      because the mock is where the id mapping is asserted;
//   3. a boxless format that the reader cannot open dead-letters at text_not_read, the kind
//      whose sentence EXTR-15-04 wrote.

/** Fills the manual form the hand-off lands on, files it, and returns the new invoice's id. */
async function fileHandOffDraft(page: Page, invoiceNumber: string): Promise<string> {
  const fileBtn = page.getByRole('button', { name: 'File invoice' })
  const numberInput = page.getByPlaceholder('INV-0000-00000')
  await expect(numberInput, 'the hand-off must land on the manual entry form').toBeVisible({ timeout: 60_000 })
  await expect(page.getByRole('button', { name: 'Invoice number is required' }), 'the form starts blank').toBeDisabled()

  // A fresh number per call: (tenant_id, entity_id, invoice_number) is unique, so a
  // Playwright retry would 409 on the row its own first attempt filed.
  await numberInput.fill(invoiceNumber)
  await expect(fileBtn, 'a resolved entity and a non-blank number must arm the primary').toBeEnabled()

  const [res] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices'),
      { timeout: 60_000 },
    ),
    fileBtn.click(),
  ])
  expect(res.status(), 'the hand-off filing must be a 201').toBe(201)
  const id = ((await res.json()) as { id?: string }).id
  expect(id, 'the create response carried no invoice id').toMatch(/^[0-9a-fA-F-]{36}$/)

  await expect(page.getByTestId('invoice-detail'), 'a filed hand-off must land on the real invoice detail').toBeVisible({
    timeout: 60_000,
  })
  return id as string
}

// A narrow local type over GET /v1/invoices/{id}/source-document rather than a client.ts
// helper: frontend/app already mirrors this body key-for-key under wireMirrors.test.ts, and a
// second typed mirror here would be one more thing to keep in step for one assertion.
interface SourceDocumentBody {
  invoice_id: string
  source_rows: number[] | null
  document: { id: string; filename: string | null } | null
}

async function readSourceDocument(token: string, invoiceId: string): Promise<SourceDocumentBody> {
  const res = await fetch(`${apiBase()}/api/invoice/v1/invoices/${invoiceId}/source-document`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(res.status, `GET /v1/invoices/${invoiceId}/source-document`).toBe(200)
  return (await res.json()) as SourceDocumentBody
}

/**
 * Runs N documents through the wizard in ONE run and returns each one's stored id and the job
 * it settled on. Asserts NO landing: the three tests below each land somewhere different (the
 * invoice detail, the review batch surface, the failures card), so a helper that picked one
 * would be wrong for the other two — each states its own.
 */
async function runDocuments(
  page: Page,
  label: string,
  files: { name: string; mimeType: string; buffer: Buffer }[],
): Promise<{
  token: string
  entityId: string
  documentIds: Record<string, string>
  jobs: Record<string, ExtractionJob>
}> {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `${label} ${Date.now()}`, tin: freshTin() })

  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)

  return { token, entityId: entity.id, ...(await runDocumentsIn(page, token, files)) }
}

/**
 * runDocuments' wizard half: pick, extract and settle N documents in the entity the page has
 * ALREADY selected. Split out for EXTR15-E2E-06 (EXTR-15-13), which needs a SECOND run in the
 * same entity — a cross-run collision is the only way the already-imported channel has a
 * determined winner.
 *
 * The route handler is registered per call. Playwright matches the most recently added handler
 * first, so an earlier run's `files` closure never sees this run's uploads.
 */
async function runDocumentsIn(
  page: Page,
  token: string,
  files: { name: string; mimeType: string; buffer: Buffer }[],
): Promise<{ documentIds: Record<string, string>; jobs: Record<string, ExtractionJob> }> {
  const documentIds: Record<string, string> = {}
  // Recorded INSIDE the POST route, EXTR10-E2E-02's own idiom: the handler completes before
  // the app can see the response, so no poll for this document can precede the assignment. A
  // page.on('response') listener cannot give that ordering.
  //
  // Named and unrouted below, never an inline closure: this helper is called twice on one page
  // by EXTR15-E2E-06, and a handler left installed by the first call still matches the second
  // call's uploads while searching them for the FIRST call's filenames -- finding none, writing
  // nothing, and fulfilling the request so the live handler never sees it.
  const captureUpload = async (route: Route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    const body = requestBody(route.request())
    const res = await route.fetch()
    const text = await res.text()
    try {
      const parsed = JSON.parse(text) as { document_id?: string }
      // The multipart body carries `filename="…"` in ASCII near its head, so this resolves
      // even though the rest of it is binary.
      const named = files.find((f) => body.includes(f.name))
      if (named && typeof parsed.document_id === 'string') documentIds[named.name] = parsed.document_id
    } catch {
      /* fulfil unchanged; the poll below fails loudly if the upload really broke */
    }
    await route.fulfill({ response: res, body: text })
  }
  await page.route('**/api/submission/v1/documents', captureUpload)

  // An earlier run on this page routes LATE: past the import 201 a caller polls for. The
  // progress card unmounts on applyRoute.
  await expect(
    page.getByTestId('import-progress'),
    'an earlier run on this page has not routed yet',
  ).toHaveCount(0, { timeout: 60_000 })

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  await page.locator('input[type="file"]#pf-import-file').setInputFiles(files)
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  // expect.poll's message is a string fixed at call time, so the URL at failure is appended here:
  // a late route from an earlier run shows up as the page sitting on another screen.
  await expect
    .poll(() => Object.keys(documentIds).length, {
      message: 'not every document reached storage',
      timeout: 120_000,
    })
    .toBe(files.length)
    .catch((e: Error) => {
      throw new Error(`${e.message}\ncaptured ${JSON.stringify(documentIds)}; page was at ${page.url()}`, { cause: e })
    })
  // Every upload is captured, so the handler has nothing left to do and must not outlive this
  // call -- see captureUpload's own note.
  await page.unroute('**/api/submission/v1/documents', captureUpload)

  const jobs: Record<string, ExtractionJob> = {}
  for (const f of files) {
    await expect
      .poll(
        async () => {
          const { jobs: found } = await getExtractions(token, documentIds[f.name])
          const job = found[0]
          if (!job) return 'no job for this document yet'
          jobs[f.name] = job
          // 'failed' is NOT terminal — River retries it; only succeeded/dead_lettered end it.
          return job.state === 'succeeded' || job.state === 'dead_lettered' ? 'terminal' : job.state
        },
        { message: `${f.name}'s extraction never reached a terminal state`, timeout: 240_000, intervals: [1_000] },
      )
      .toBe('terminal')
  }
  return { documentIds, jobs }
}

const GOLDEN_DOCX_NAME = 'golden_invoice.docx'
const EMPTY_DOCX_NAME = 'empty_container.docx'

test('EXTR15-E2E-02 (AC-6): a two-document run hands off the row that was clicked, not its sibling', async ({
  page,
}) => {
  // Two documents through a cold sidecar, then a filing and a detail load.
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const scannedName = 'scanned_invoice.pdf'
  const denseName = 'dense_invoice.pdf'
  // Both quarantine, for DIFFERENT measured reasons, and neither needs a new artifact: the
  // scan settles document_text_layer = unreadable with no number at all, and the dense page's
  // label OCRs as "INV0ICE NO:" so its invoice_number settles missing (D-33). They carry
  // different filenames and different document ids, so the two rows are genuinely
  // distinguishable — a pair whose items are naturally equal proves nothing.
  const { token, documentIds } = await runDocuments(page, 'EXTR-15-12 two documents', [
    { name: scannedName, mimeType: 'application/pdf', buffer: uniqueScannedPdfBytes() },
    { name: denseName, mimeType: 'application/pdf', buffer: uniqueDensePdfBytes() },
  ])

  expect(
    documentIds[scannedName],
    'both files resolved to ONE stored document -- the pair cannot discriminate anything below',
  ).not.toBe(documentIds[denseName])

  // (a) routeAfterRun cannot take the 'single' arm on a two-file run (lib/importRun.ts), so
  // the landing is the review surface carrying EVERY batch id. The path is where that id list
  // is observable: App.tsx mirrors reviewBatchIds into it, one id per batch.
  await expect
    .poll(() => new URL(page.url()).pathname, {
      message: 'a two-document run must land on the review batch surface with both batch ids',
      timeout: 180_000,
    })
    .toMatch(/^\/imports\/[0-9a-fA-F-]{36},[0-9a-fA-F-]{36}\/review$/)

  // (b) both documents reach the Unreadable tab, told apart by their own file labels. A
  // tab-level scalar document id would pass a same-document pair; two labelled rows cannot.
  // The tab is matched on its prefix, not on its count: the count is a second statement of
  // what toHaveCount below already asserts, and matching it here would fail one step earlier
  // with a locator error instead of a row count.
  const unreadableTab = page.getByRole('button', { name: /^Quarantined documents \(/ })
  await expect(unreadableTab, 'neither document quarantined -- there is no unreadable tab').toHaveCount(1)
  await unreadableTab.click()

  const unreadableRows = page.getByTestId('unreadable-row')
  await expect(unreadableRows, 'both documents must render as their own rows').toHaveCount(2)
  const rowLabels = await unreadableRows.evaluateAll((els) => els.map((el) => el.textContent ?? ''))
  const scannedIdx = rowLabels.findIndex((t) => t.includes(scannedName))
  const denseIdx = rowLabels.findIndex((t) => t.includes(denseName))
  expect(scannedIdx, `no row names ${scannedName}: ${JSON.stringify(rowLabels)}`).toBeGreaterThanOrEqual(0)
  expect(denseIdx, `no row names ${denseName}: ${JSON.stringify(rowLabels)}`).toBeGreaterThanOrEqual(0)
  expect(scannedIdx, 'one row names both files -- the labels do not discriminate the rows').not.toBe(denseIdx)

  // Cross-discriminating pair: bare "scan" is vacuous (the row text also carries the
  // filename scanned_invoice.pdf), so "supplier" tells the two RowError messages apart.
  expect(rowLabels[scannedIdx], 'the scanned row must carry the scan-quality message').toContain('The scan of this document')
  expect(rowLabels[scannedIdx], 'the scanned row must carry the supplier ask').toContain('supplier')
  expect(rowLabels[scannedIdx], "the scanned row leaked the dense row's message").not.toContain('was read')

  expect(rowLabels[denseIdx], 'the dense row must carry the no-invoice-number message').toContain(
    'was read, but no invoice number',
  )
  expect(rowLabels[denseIdx], "the dense row leaked the scanned row's supplier ask").not.toContain('supplier')

  // (c) the SCANNED row's own control, and the invoice it produces named by equality against
  // the scanned document — never "is not null", which the dense document would satisfy too.
  const scannedButton = unreadableRows.nth(scannedIdx).getByRole('button', { name: 'Enter it by hand' })
  await expect(scannedButton, 'a resolved entity must arm the hand-off on this row').toBeEnabled()
  await scannedButton.click()

  const invoiceNumber = `EXTR15-MULTI-${Date.now()}`
  const invoiceId = await fileHandOffDraft(page, invoiceNumber)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(invoiceNumber)

  const source = await readSourceDocument(token, invoiceId)
  expect(source.document, 'the filed invoice names no source document at all').not.toBeNull()
  expect(source.document!.id, "the hand-off attached the wrong row's document").toBe(documentIds[scannedName])
  expect(source.document!.id, 'the hand-off attached the sibling document').not.toBe(documentIds[denseName])

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

const NUMBER_TAKEN_REASON = 'This invoice number is already in the register for this company. Enter a different number.'
const CARRIED_CAPTION = 'Read from the document. Enter the invoice number to file it.'
const SUPPLY_URL = /\/api\/invoice\/v1\/imports\/document\/invoice$/

test('EXTR27-E2E-01: a read document with no number carries its reading into the hand-off, refuses a taken number, and files with the one supplied', async ({
  page,
}) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page, expectedStatusDropper(page, 409, SUPPLY_URL))

  const denseName = 'dense_invoice.pdf'
  // Zz sorts this entity after "Okafor & Partners" (carry note) so later topology specs keep
  // their own ordering assumptions.
  const { token, entityId, documentIds, jobs } = await runDocuments(page, 'Zz EXTR-27 carried', [
    { name: denseName, mimeType: 'application/pdf', buffer: uniqueDensePdfBytes() },
  ])
  const documentId = documentIds[denseName]!
  expect(
    jobs[denseName]!.state,
    `the dense page did not settle succeeded (kind ${jobs[denseName]!.failure_kind}, error ${jobs[denseName]!.last_error})`,
  ).toBe('succeeded')

  // 1. Landing (AC-1).
  await expect
    .poll(() => new URL(page.url()).pathname, { timeout: 180_000 })
    .toMatch(/^\/imports\/[0-9a-fA-F-]{36}\/review$/)

  const unreadableTab = page.getByRole('button', { name: /^Quarantined documents \(/ })
  await expect(unreadableTab).toHaveCount(1)
  await unreadableTab.click()

  const row = page.getByTestId('unreadable-row')
  await expect(row).toHaveCount(1)
  await expect(row).toContainText(denseName)
  await expect(row).toContainText('was read, but no invoice number')

  // 2. Non-empty floor — the reading must carry something, or a carried and a blank form
  // would be indistinguishable below.
  const reading = await getCarriedReading(token, documentId)
  expect(reading, 'a read no-number document carried no reading').not.toBeNull()
  const HEADER = ['issue_date', 'buyer_name', 'buyer_tin', 'currency', 'subtotal', 'vat', 'total'] as const
  expect(
    HEADER.filter((k) => reading![k] !== null).length + reading!.line_items.length,
    'the reading carried nothing, so the row cannot tell a carried form from a blank one',
  ).toBeGreaterThan(0)

  // 3. Carried form (AC-2).
  const readingGet = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/document/reading'),
    { timeout: 60_000 },
  )
  const handOff = row.getByRole('button', { name: 'Enter it by hand' })
  await expect(handOff).toBeEnabled()
  await handOff.click()
  expect((await readingGet).status()).toBe(200)

  await expect(page.getByText(CARRIED_CAPTION)).toBeVisible({ timeout: 60_000 })
  const numberInput = page.getByPlaceholder('INV-0000-00000')
  await expect(numberInput).toHaveValue('')
  await expect(page.getByRole('button', { name: 'Invoice number is required' })).toBeDisabled()

  for (const k of ['issue_date', 'buyer_name', 'buyer_tin', 'currency'] as const) {
    const field = page.getByTestId(`carried-${k}`)
    await expect(field).toHaveValue(reading![k] ?? '')
    await expect(field).not.toBeEditable()
    // A null reading never becomes an empty box: it shows the same '—' cue as the money cells.
    if (reading![k] === null) {
      await expect(field).toHaveAttribute('placeholder', '—')
    } else {
      await expect(field).not.toHaveAttribute('placeholder', '—')
    }
  }
  for (const k of ['subtotal', 'vat', 'total'] as const) {
    await expect(page.getByTestId(`carried-${k}`)).toHaveText(reading![k] ?? '—')
  }
  const lineRows = page.getByTestId('carried-line-row')
  await expect(lineRows).toHaveCount(reading!.line_items.length)
  // Positional, matching CreateFlow.test.tsx's own line-cell convention: no per-field testid exists.
  for (let i = 0; i < reading!.line_items.length; i++) {
    const line = reading!.line_items[i]!
    const inputs = lineRows.nth(i).locator('input')
    const cells = [
      ['description', line.description],
      ['quantity', line.quantity],
      ['unit_price', line.unit_price],
    ] as const
    for (const [idx, [field, value]] of cells.entries()) {
      const input = inputs.nth(idx)
      await expect(input, `carried line ${i} ${field}`).toHaveValue(value ?? '')
      if (value === null) {
        await expect(input).toHaveAttribute('placeholder', '—')
      } else {
        await expect(input).not.toHaveAttribute('placeholder', '—')
      }
    }
  }

  // 4. Taken number refused (AC-5).
  const taken = `EXTR27-TAKEN-${Date.now()}`
  await createInvoice(token, { entity_id: entityId, invoice_number: taken })

  await numberInput.fill(taken)
  const fileBtn = page.getByRole('button', { name: 'File invoice' })
  await expect(fileBtn).toBeEnabled()

  const isSupply = (r: Response) => r.request().method() === 'POST' && SUPPLY_URL.test(new URL(r.url()).pathname)
  const refused = page.waitForResponse(isSupply, { timeout: 60_000 })
  await fileBtn.click()
  expect((await refused).status()).toBe(409)

  await expect(page.getByText(NUMBER_TAKEN_REASON, { exact: true })).toBeVisible()
  await expect(numberInput).toHaveValue(taken)
  await expect(fileBtn).toBeEnabled()
  await expect(page.getByText(CARRIED_CAPTION), 'the refusal left carried mode').toBeVisible()
  for (const k of ['issue_date', 'buyer_name', 'buyer_tin', 'currency'] as const) {
    await expect(page.getByTestId(`carried-${k}`), `the refusal changed carried-${k}`).toHaveValue(reading![k] ?? '')
  }
  for (const k of ['subtotal', 'vat', 'total'] as const) {
    await expect(page.getByTestId(`carried-${k}`), `the refusal changed carried-${k}`).toHaveText(reading![k] ?? '—')
  }
  await expect(page.getByTestId('carried-line-row')).toHaveCount(reading!.line_items.length)
  await expect(page.getByTestId('invoice-detail')).toHaveCount(0)

  // 5. Filed (AC-3).
  const fresh = `EXTR27-${Date.now()}`
  await numberInput.fill(fresh)
  const filed = page.waitForResponse(isSupply, { timeout: 60_000 })
  await fileBtn.click()
  const res = await filed
  expect(res.status()).toBe(201)
  const id = ((await res.json()) as { id?: string }).id
  expect(id).toMatch(/^[0-9a-fA-F-]{36}$/)

  await expect(page.getByTestId('invoice-detail')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(fresh)

  // 6. Every carried value intact, validation ran (AC-3, AC-6).
  const inv = await getInvoice(token, id!)
  expect(inv.invoice_number).toBe(fresh)
  expect((inv.issue_date ?? '').slice(0, 10) || null).toBe(reading!.issue_date)
  expect(inv.buyer_name).toBe(reading!.buyer_name)
  expect(inv.buyer_tin).toBe(reading!.buyer_tin)
  expect(inv.currency).toBe(reading!.currency)
  // Money is compared at 2dp: numeric(14,2) reads a whole reading value back padded.
  const at2 = (v: string | null) => (v === null ? null : Number(v).toFixed(2))
  for (const k of ['subtotal', 'vat', 'total'] as const) {
    expect(at2(inv[k])).toBe(at2(reading![k]))
  }
  expect((inv.line_items ?? []).length).toBe(reading!.line_items.length)
  expect(inv.rule_set_version_id, 'the supply filed a draft no rule set ever judged').not.toBeNull()
  expect((await readSourceDocument(token, id!)).document?.id).toBe(documentId)

  // 7. Audit (AC-7).
  const { events } = await getAuditLog(token, { invoice_id: id!, event: ['invoice.created'], limit: 100 })
  expect(events).toHaveLength(1)
  const payload = events[0]!.payload as Record<string, unknown>
  expect(payload.invoice_number_supplied).toBe(true)
  expect(payload.document_id).toBe(documentId)
  // A row's kind is person|system|raw; 'people' is only the filter value.
  expect(events[0]!.actor_kind).toBe('person')

  // 8.
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

/**
 * Waits for a document run to come to REST and names the surface it stopped on. An
 * OBSERVATION: the caller attaches the answer rather than asserting it, so a routing change
 * moves a recorded string instead of reddening a case about extraction.
 */
async function settledRunSurface(page: Page): Promise<string> {
  let seen = 'nothing yet'
  await expect
    .poll(
      async () => {
        if (await page.getByTestId('extraction-review').isVisible()) seen = 'extraction review'
        else if (await page.getByTestId('invoice-detail').isVisible()) seen = 'invoice detail'
        else if (await page.getByTestId('review-table').isVisible()) seen = 'review batch surface'
        else if (await page.getByText(/^BATCH /).first().isVisible()) seen = 'review batch surface'
        else if (await page.getByTestId('document-failures-card').isVisible()) seen = 'the failures card'
        else seen = 'nothing yet'
        return seen !== 'nothing yet'
      },
      { message: 'the run never came to rest on any surface', timeout: 180_000 },
    )
    .toBe(true)
  return seen
}

test('EXTR15-E2E-03: the deployed sidecar reads a real DOCX, and reads its printed fields', async ({ page }, testInfo) => {
  // One DOCX on a fleet that may be cold, then the import and the landing.
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const { token, jobs } = await runDocuments(page, 'EXTR-15-12 docx', [
    { name: GOLDEN_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueGoldenDocxBytes() },
  ])
  const job = jobs[GOLDEN_DOCX_NAME]
  expect(
    job.state,
    `the DOCX did not settle succeeded (kind ${job.failure_kind ?? 'null'}, error ${job.last_error ?? 'none'})`,
  ).toBe('succeeded')

  const detail = await getExtractionDetail(token, job.id)
  // Zero pages is the CONTRACT, not an absence of evidence: pageImageFormats marks DOCX
  // boxless (classify.go), so worker.go skips the render and the page rows. Since EXTR-19-04 it
  // does write a b1 layout -- a column on the job, never a page row.
  expect(detail.pages, 'a boxless format must write no page rows').toEqual([])

  // Equality on all three, never a negation: a reachable-but-EMPTY sidecar settles succeeded
  // with every field absent, and `undefined !== 'MOCK-INV-0001'` passes on exactly that. Three
  // fields, so one lucky read cannot carry the case. The values are the Go golden's
  // (corpus_wired_db_test.go's TestRLS_DocxFixtureResolvesNamedFields), which was recorded
  // through a local docling -- this is the first read of them by the deployed one.
  const valueOf = (name: string) => {
    const f = detail.fields.find((x) => x.name === name)
    expect(f, `no ${name} field on the wire (fields: ${detail.fields.map((x) => x.name).join(', ') || 'none'})`).toBeTruthy()
    return f!.value
  }
  expect(valueOf('invoice_number'), "the settled number is not the DOCX's printed number").toBe('ASC-2026-0919')
  expect(valueOf('issue_date'), "the settled date is not the DOCX's printed date").toBe('2026-08-14')
  expect(valueOf('total'), "the settled total is not the DOCX's printed total").toBe('4300.00')

  // OBSERVED, not asserted. Which surface the run lands on is routeAfterRun's business and
  // no AC of this story's; what this waits for is the run coming to REST, so the console gate
  // below reads a finished journey rather than one mid-navigation.
  const landing = await settledRunSurface(page)
  await testInfo.attach('docx-run-landing.txt', { body: landing, contentType: 'text/plain' })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR15-E2E-04 (T3): a DOCX the reader cannot open dead-letters at text_not_read', async ({ page }) => {
  // Upload plus River's three attempts at attempt^4s backoff, each one a fast 422.
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  const { jobs } = await runDocuments(page, 'EXTR-15-12 empty docx', [
    { name: EMPTY_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueEmptyDocxBytes() },
  ])
  const job = jobs[EMPTY_DOCX_NAME]
  expect(job.state, 'a DOCX the reader refuses must dead-letter, not succeed').toBe('dead_lettered')

  // EQUALITY, never "not pages_not_rendered": five other kinds satisfy that negation, and the
  // whole claim here is that the render stage never ran at all for a boxless format.
  expect(
    job.failure_kind,
    `the kind was ${job.failure_kind ?? 'null'} (error ${job.last_error ?? 'none'})`,
  ).toBe('text_not_read')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-15-13 (task-857) · AC-2/AC-3: the document unit, rendered and contained --------
//
// AC-2 is EXTR15-E2E-05's mirror: the DOCUMENT arm of the same census sites, on the deployed
// build. AC-3 is the containment sweep over what this story LENGTHENED -- the hand-off control
// subtask 11 put in the Unreadable tab, and the two "Not imported" tiles whose text grew
// ("already in the register" is six characters longer than "already imported").
//
// ONE test for both, because the journey is the expensive part: three extractions on a fleet
// that may be cold. EXTR15-E2E-01 folds its AC-5 walk into its geometry sweep for the same
// reason.
//
// TWO RUNS IN ONE ENTITY, and that ordering IS the oracle. The already-imported channel needs
// the colliding invoice to exist BEFORE the second import runs. Two copies inside one run
// would race -- startDocumentRun imports through Promise.all (lib/documentRun.ts) -- and while
// the SHAPE would still hold (storeDuplicateRowError sets rule_key on both the up-front
// precheck and the racing-INSERT backstop, and isAlreadyImported reads only that key), nothing
// would say WHICH file landed in which channel, and this case names files.
//
// Run 2's second file is scanned_invoice.pdf, EXTR15-E2E-02's own choice and for its reason:
// it settles SUCCEEDED with no readable number, so the import quarantines it into the
// unreadable channel with its document id attached (EXTR-15-10) -- which is what puts a
// hand-off control on the row at all. A file that dead-letters produces no batch and never
// reaches this screen.
//
// NO LOCAL ORACLE, like every case in this span. If the deployed sidecar reads either fixture
// differently from EXTR15-E2E-02/03's measurements, this reds on a job state or a tab count
// with a message naming which.

const REPEAT_DOCX_NAME = 'golden_invoice_again.docx'
const QUARANTINED_PDF_NAME = 'scanned_invoice.pdf'
// The golden's printed number, the same literal EXTR15-E2E-03 asserts off the wire.
const GOLDEN_DOCX_NUMBER = 'ASC-2026-0919'

test('EXTR15-E2E-06 (AC-2/AC-3): the document review screen says documents and register, and holds its controls at every width', async ({
  page,
}, testInfo) => {
  // Three extractions across two runs on a fleet that may be cold, then a four-width sweep.
  test.setTimeout(900_000)
  const errors = collectErrors(page)

  // --- run 1: one DOCX, whose invoice is what run 2 collides with ------------------------
  const { token, entityId, jobs: seedJobs } = await runDocuments(page, 'EXTR-15-13 register', [
    { name: GOLDEN_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueGoldenDocxBytes() },
  ])
  expect(
    seedJobs[GOLDEN_DOCX_NAME].state,
    `the seeding DOCX did not settle succeeded (kind ${seedJobs[GOLDEN_DOCX_NAME].failure_kind ?? 'null'}, error ${seedJobs[GOLDEN_DOCX_NAME].last_error ?? 'none'})`,
  ).toBe('succeeded')

  // The precondition read off the REGISTER, not off the landing: the invoice existing is the
  // whole basis of run 2. Listed by entity rather than searched by `q`, so nothing here depends on
  // the search predicate's matching rules.
  // POLLED, not read once: a settled extraction is not a filed invoice. The SPA calls the
  // import endpoint AFTER the job reaches its terminal state, so a single read here races the
  // write it is checking for and returns an empty register.
  await expect
    .poll(
      async () => (await listInvoices(token, { entity_id: entityId, limit: 50 })).invoices.map((i) => i.invoice_number),
      {
        message: 'run 1 stored no invoice under the golden number -- run 2 has nothing to collide with',
        timeout: 120_000,
        intervals: [1_000],
      },
    )
    .toContain(GOLDEN_DOCX_NUMBER)

  // Run 1 routes to its review AFTER the import lands; starting run 2 earlier lets that late
  // navigation replace run 2's wizard before it uploads anything.
  await expect
    .poll(() => new URL(page.url()).pathname, {
      message: "run 1 never landed on its extraction review -- run 2 would race run 1's late route",
      timeout: 180_000,
    })
    .toMatch(/^\/extraction\/[0-9a-fA-F-]{36}$/)
  await expect(page.getByTestId('extraction-review-body')).toBeVisible()

  // --- run 2: the same document again, beside one that quarantines -----------------------
  const { jobs } = await runDocumentsIn(page, token, [
    { name: REPEAT_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueGoldenDocxBytes() },
    { name: QUARANTINED_PDF_NAME, mimeType: 'application/pdf', buffer: uniqueScannedPdfBytes() },
  ])
  expect(jobs[REPEAT_DOCX_NAME].state, 'the repeat DOCX must read as well as the first one did').toBe('succeeded')
  expect(
    jobs[QUARANTINED_PDF_NAME].state,
    `the scan settled ${jobs[QUARANTINED_PDF_NAME].state} (kind ${jobs[QUARANTINED_PDF_NAME].failure_kind ?? 'null'}) -- a dead-letter writes no batch and never reaches the review screen`,
  ).toBe('succeeded')

  // Two files cannot take routeAfterRun's 'single' arm, so the landing is the review surface
  // carrying both batch ids -- EXTR15-E2E-02's own reading of the path.
  await expect
    .poll(() => new URL(page.url()).pathname, {
      message: 'a two-document run must land on the review batch surface with both batch ids',
      timeout: 180_000,
    })
    .toMatch(/^\/imports\/[0-9a-fA-F-]{36},[0-9a-fA-F-]{36}\/review$/)

  // --- AC-2: the header, the tiles and both tab labels ----------------------------------
  const registerTabName = 'Already imported (1)'
  const unreadableTabName = 'Quarantined documents (1)'
  await expect(
    page.getByRole('button', { name: unreadableTabName }),
    'the scan must quarantine into its own tab, labelled in documents',
  ).toHaveCount(1)
  await expect(
    page.getByRole('button', { name: registerTabName }),
    'the repeat DOCX must collide into the already-imported channel',
  ).toHaveCount(1)
  await expect(
    page.getByRole('button', { name: /^Unreadable rows \(/ }),
    'the spreadsheet tab label reached a document run',
  ).toHaveCount(0)

  const header = await screenText(page)
  expect(header, 'the batch header must count documents (R1/R2)').toContain(`2 ${DOCUMENT_READ_LINE}`)
  expect(header, 'the spreadsheet branch reached a document run').not.toContain(SPREADSHEET_READ_LINE)
  expect(header, "B1's document arm").toContain('Built from 0 documents. Every one of these exists in the ledger')
  expect(header, "B1's spreadsheet arm reached a document run").not.toContain(
    'rows. Every one of these exists in the ledger',
  )
  expect(header, "B8's document arm -- the AC's own wording").toContain('1 already in the register')
  expect(header, "B10's document arm").toContain('1 invoices already in the register. Nothing to fix.')
  expect(header, 'the spreadsheet ledger wording reached a document run').not.toContain('already in your ledger')
  expect(header, "B2's document arm").toContain('1 quarantined documents')
  expect(header, "B2's spreadsheet arm reached a document run").not.toContain('1 unreadable rows')
  expect(header, 'the shipped unreadable wording reached a document run').not.toContain('unreadable documents')
  expect(header, "B11's document arm").toContain(
    'A structural failure, not a compliance one: no rule was ever run and no invoice was created. The documents themselves are still stored.',
  )
  expect(header, "B11's spreadsheet arm reached a document run").not.toContain('Nothing was stored')

  // --- AC-2: the already-imported tab body ----------------------------------------------
  await page.getByRole('button', { name: registerTabName }).click()
  const register = await screenText(page)
  expect(register, "A3's document arm").toContain('1 documents were already in the register')
  expect(register, "A4's document arm").toContain(
    'These documents are already in the register, so the import had nothing new to add. Nothing is wrong with them and there is nothing to correct.',
  )
  expect(register, "A7's document arm").toContain('1 of 2 documents were already in the register.')
  expect(register, 'the spreadsheet ledger wording reached a document run').not.toContain('already in your ledger')

  // A5/A6: the grid header, resolved from a sibling cell for the reason EXTR15-E2E-05 gives.
  const registerVerdictCell = page.getByText('Invoice already in the register', { exact: true })
  await expect(registerVerdictCell, "the already-imported grid's verdict header (A6)").toHaveCount(1)
  const registerHeaderRow = registerVerdictCell.locator('xpath=..')
  await expect(
    registerHeaderRow.getByText('File', { exact: true }),
    'the resolved element is the header row, not the card around it',
  ).toHaveCount(1)
  await expect(registerHeaderRow.getByText('—', { exact: true }), "A5's document arm").toHaveCount(1)
  await expect(
    registerHeaderRow.getByText('Row', { exact: true }),
    "A5's spreadsheet arm reached a document run",
  ).toHaveCount(0)

  // --- AC-2: the unreadable tab body, and the row AC-3 then measures ---------------------
  await page.getByRole('button', { name: unreadableTabName }).click()
  const unreadable = await screenText(page)
  expect(unreadable, "U2's document arm").toContain('1 documents never became invoices')
  expect(unreadable, "U3's document arm").toContain(
    'No invoice was created from them, so no rule was ever run against them. Each document is still stored, and the list below says what stopped it: enter that invoice by hand, or replace the document and import again.',
  )
  expect(unreadable, "U3's spreadsheet arm reached a document run").not.toContain('The importer could not read them')
  expect(unreadable, "U5's document arm").toContain('1 of 2 documents. The invoices that did import are unaffected.')

  const whyCell = page.getByText('Why it could not be read', { exact: true })
  await expect(whyCell, "the unreadable grid's last header cell").toHaveCount(1)
  const unreadableHeaderRow = whyCell.locator('xpath=..')
  await expect(
    unreadableHeaderRow.getByText('File', { exact: true }),
    'the resolved element is the header row, not the card around it',
  ).toHaveCount(1)
  await expect(unreadableHeaderRow.getByText('—', { exact: true }), "U4's document arm").toHaveCount(1)
  await expect(
    unreadableHeaderRow.getByText('Row', { exact: true }),
    "U4's spreadsheet arm reached a document run",
  ).toHaveCount(0)

  // --- AC-3: the containment sweep ------------------------------------------------------
  //
  // Non-empty FIRST, every time: an empty locator satisfies every containment check below
  // vacuously, and on a broken deploy that is exactly how this sweep would go green.
  const row = page.getByTestId('unreadable-row')
  await expect(row, 'the scan must render as its own row').toHaveCount(1)
  await expect(row.first(), 'the row must name the file it came from').toContainText(QUARANTINED_PDF_NAME)
  const card = row.first().locator('xpath=..')
  await expect(card, 'the resolved parent is the grid card, which also carries the header').toContainText(
    'Why it could not be read',
  )
  const handOff = row.first().getByRole('button', { name: 'Enter it by hand' })
  await expect(handOff, 'a stored document must offer the hand-off (EXTR-15-11)').toHaveCount(1)

  // The two tiles this story lengthened, each resolved from its VALUE text up to the Tile root
  // (ReviewBatch.tsx's Tile renders value and caption as the two children of one div). Each
  // chain is self-checked by the caption its tile must also carry.
  const registerTileValue = page.getByText('1 already in the register', { exact: true })
  await expect(registerTileValue, "B8's tile value").toHaveCount(1)
  const registerTile = registerTileValue.locator('xpath=..')
  await expect(registerTile, 'the resolved parent is the tile, which also carries its caption').toContainText(
    'Nothing to fix.',
  )
  const unreadableTileValue = page.getByText('1 quarantined documents', { exact: true })
  await expect(unreadableTileValue, "B2's tile value").toHaveCount(1)
  const unreadableTile = unreadableTileValue.locator('xpath=..')
  await expect(unreadableTile, 'the resolved parent is the tile, which also carries its caption').toContainText(
    'No invoice exists for them.',
  )

  type Fit = { width: number; rowSlack: number; handOffSlack: number; registerSlack: number; unreadableSlack: number }
  const fits: Fit[] = []
  const entryViewport = page.viewportSize()

  try {
    // Widest first -- WIDE_WIDTHS' own order (layout.ts): a cap strands only what the window
    // gives it room to strand.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const read = async () => {
        const [cardBox, rowBox, handOffBox, rowEdges, registerText, registerBox, unreadableText, unreadableBox] =
          await Promise.all([
            card.boundingBox(),
            row.first().boundingBox(),
            handOff.boundingBox(),
            row.first().evaluate(edgesOf),
            registerTileValue.evaluate(edgesOf),
            registerTile.evaluate(edgesOf),
            unreadableTileValue.evaluate(edgesOf),
            unreadableTile.evaluate(edgesOf),
          ])
        return { cardBox, rowBox, handOffBox, rowEdges, registerText, registerBox, unreadableText, unreadableBox }
      }
      const m = await settledRead(read, `document review screen at ${width}px`)
      expect(m.cardBox && m.rowBox && m.handOffBox, `card, row and control must all render at ${width}px`).toBeTruthy()

      // (a) the row is CONTAINED by its card -- the intersection is the row's own rect.
      // rectsOverlap (layout.ts) is a boolean and is true for a row hanging half out, so
      // it cannot state this at all; overlapOf returns the Rect that can.
      expect(
        sameRect(overlapOf(m.rowBox!, m.cardBox!), m.rowBox!),
        `the row must sit wholly inside its card at ${width}px (row ${JSON.stringify(m.rowBox)}, card ${JSON.stringify(m.cardBox)})`,
      ).toBe(true)

      // (b) the hand-off control is contained by its row, the same identity. This is the
      // subtask-11 control, and it lives inside the row's 1fr track -- the one track that can
      // be squeezed to nothing.
      expect(
        sameRect(overlapOf(m.handOffBox!, m.rowBox!), m.handOffBox!),
        `the hand-off control must sit wholly inside its row at ${width}px (button ${JSON.stringify(m.handOffBox)}, row ${JSON.stringify(m.rowBox)})`,
      ).toBe(true)

      // (c) the row's four grid tracks fit the box the card gave it. (a) cannot see this: a
      // grid container keeps its own width while its tracks overflow, so the box stays inside
      // the card while the cells hang out of it.
      expect(
        m.rowEdges.scrollWidth,
        `the row's grid tracks overflow their own box at ${width}px (${m.rowEdges.scrollWidth} > ${m.rowEdges.clientWidth})`,
      ).toBeLessThanOrEqual(m.rowEdges.clientWidth + 1)

      // (d) each lengthened tile value stays inside its tile, and its TEXT fits the box it was
      // given. Both, never one: a block child keeps its parent's content width while the text
      // inside it overflows, so the edge check alone passes on the very defect this guards.
      for (const [label, text, tile] of [
        ['the already-in-the-register tile', m.registerText, m.registerBox],
        ['the quarantined-documents tile', m.unreadableText, m.unreadableBox],
      ] as const) {
        expect(text.outerLeft, `${label}'s value must start inside its tile at ${width}px`).toBeGreaterThanOrEqual(
          tile.left - 0.5,
        )
        expect(text.outerRight, `${label}'s value must end inside its tile at ${width}px`).toBeLessThanOrEqual(
          tile.right + 0.5,
        )
        expect(
          text.scrollWidth,
          `${label}'s value text overflows its own box at ${width}px (${text.scrollWidth} > ${text.clientWidth})`,
        ).toBeLessThanOrEqual(text.clientWidth + 1)
      }

      fits.push({
        width,
        rowSlack: m.rowEdges.clientWidth - m.rowEdges.scrollWidth,
        handOffSlack: m.rowEdges.right - (m.handOffBox!.x + m.handOffBox!.width),
        registerSlack: m.registerBox.right - m.registerText.outerRight,
        unreadableSlack: m.unreadableBox.right - m.unreadableText.outerRight,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  // The sweep ran every width, in order -- a loop that measured fewer would otherwise pass on
  // whatever it did reach.
  expect(fits.map((f) => f.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])
  await testInfo.attach('document-review-containment.json', {
    body: JSON.stringify(fits, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

const RICH_PDF_NAME = 'rich_invoice.pdf'
const RICH_PDF_NUMBER = 'ASC-2026-0918'

test('EXTR30-E2E-01 (AC-2/AC-4): a document run counts its unvalidated invoices in their own tile and never says they passed', async ({
  page,
}, testInfo) => {
  // Two extractions on a possibly cold sidecar, a reload, then a four-width sweep.
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  // A document import runs no gate, so both invoices land unstamped -- no second run needed.
  const { token, entityId, jobs } = await runDocuments(page, 'EXTR-30 unvalidated', [
    { name: RICH_PDF_NAME, mimeType: 'application/pdf', buffer: uniquePdfBytes() },
    { name: GOLDEN_DOCX_NAME, mimeType: DOCX_MIME, buffer: uniqueGoldenDocxBytes() },
  ])
  expect(
    jobs[RICH_PDF_NAME].state,
    `the rich PDF did not settle succeeded (kind ${jobs[RICH_PDF_NAME].failure_kind ?? 'null'}, error ${jobs[RICH_PDF_NAME].last_error ?? 'none'})`,
  ).toBe('succeeded')
  expect(
    jobs[GOLDEN_DOCX_NAME].state,
    `the golden DOCX did not settle succeeded (kind ${jobs[GOLDEN_DOCX_NAME].failure_kind ?? 'null'}, error ${jobs[GOLDEN_DOCX_NAME].last_error ?? 'none'})`,
  ).toBe('succeeded')

  // Vacuity guard: both invoices must exist in the register before any tile is read.
  await expect
    .poll(
      async () =>
        (await listInvoices(token, { entity_id: entityId, limit: 50 })).invoices.map((i) => i.invoice_number).sort(),
      {
        message: 'a document run holding two unevaluated drafts must still list both in the register',
        timeout: 120_000,
        intervals: [1_000],
      },
    )
    .toEqual([RICH_PDF_NUMBER, GOLDEN_DOCX_NUMBER])

  // Two documents cannot take routeAfterRun's 'single' arm, so the landing carries both
  // batch ids -- EXTR15-E2E-02's own reading of the path.
  await expect
    .poll(() => new URL(page.url()).pathname, {
      message: 'a two-document run must land on the review batch surface with both batch ids',
      timeout: 180_000,
    })
    .toMatch(/^\/imports\/[0-9a-fA-F-]{36},[0-9a-fA-F-]{36}\/review$/)
  const landingMatch = new URL(page.url()).pathname.match(/^\/imports\/([0-9a-fA-F-]{36}),([0-9a-fA-F-]{36})\/review$/)
  expect(landingMatch, 'the landing path must carry two batch ids').toBeTruthy()
  const [, batchId1, batchId2] = landingMatch!
  // The landing's shell settles first, or the listener below can catch its pre-reload response.
  await expect(page.getByRole('heading', { name: '2 invoices imported', exact: true })).toBeVisible({ timeout: 60_000 })

  // The count oracle is the wire, never the DOM: registered BEFORE the reload (INVCR-E2E-6
  // idiom), so the response the reload fires cannot be missed.
  const notEvaluatedResponse = page.waitForResponse(
    (r) =>
      r.request().method() === 'GET' &&
      new URL(r.url()).pathname.endsWith('/api/invoice/v1/invoices') &&
      new URL(r.url()).searchParams.get('not_evaluated') === 'true',
  )
  await page.reload()
  await expect(page.locator('[title="Tenant verified via /v1/me"]')).toBeAttached()
  const resp = await notEvaluatedResponse

  expect(resp.status(), 'the not_evaluated count request must succeed').toBe(200)
  const respUrl = new URL(resp.url())
  expect(
    respUrl.searchParams.getAll('import_batch_id').sort(),
    'the shell must scope the count to both batches',
  ).toEqual([batchId1, batchId2].sort())
  expect(respUrl.searchParams.get('limit'), 'the count is a pagination total, not a page of rows').toBe('1')
  const neTotal = ((await resp.json()) as { pagination: { total: number } }).pagination.total
  expect(neTotal, 'a document import runs no gate, so both invoices are unevaluated').toBe(2)

  // AC-5: the header and the three tile values.
  await expect(page.getByRole('heading', { name: '2 invoices imported', exact: true })).toBeVisible({ timeout: 60_000 })
  for (const value of ['0 valid', '0 failed a rule', `${neTotal} not yet validated`]) {
    await expect(page.getByText(value, { exact: true }), `expected exactly one "${value}"`).toHaveCount(1)
  }

  // AC-7: the layout sweep runs before any row expands below.
  const column = page.getByText('Imported · stored in the ledger', { exact: true }).locator('xpath=..')
  await expect(column, 'the resolved parent is the Imported column').toContainText('Built from 2 documents')

  const tileSpecs = [
    ['0 valid', 'Passed every rule.'],
    ['0 failed a rule', 'Read fine, stored, but not compliant yet.'],
    [`${neTotal} not yet validated`, 'Stored, but no rule has been run against them yet.'],
  ] as const
  const values = tileSpecs.map(([value]) => page.getByText(value, { exact: true }))
  const tiles = values.map((v) => v.locator('xpath=..'))
  for (const [i, [tileValue, caption]] of tileSpecs.entries()) {
    await expect(values[i], `${tileValue}'s value`).toHaveCount(1)
    await expect(tiles[i], 'the resolved parent is the tile, which also carries its caption').toContainText(caption)
  }

  type Fit = { width: number; wrapped: boolean; validSlack: number; failedSlack: number; notEvaluatedSlack: number }
  const fits: Fit[] = []
  const entryViewport = page.viewportSize()

  try {
    // Widest first -- WIDE_WIDTHS' own order (layout.ts).
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const read = async () => {
        const [columnBox, tileBoxes, tileEdges, valueEdges] = await Promise.all([
          column.boundingBox(),
          Promise.all(tiles.map((t) => t.boundingBox())),
          Promise.all(tiles.map((t) => t.evaluate(edgesOf))),
          Promise.all(values.map((v) => v.evaluate(edgesOf))),
        ])
        return { columnBox, tileBoxes, tileEdges, valueEdges }
      }
      const m = await settledRead(read, `unvalidated tile geometry at ${width}px`)
      expect(
        m.columnBox && m.tileBoxes.every(Boolean),
        `the column and all three tiles must render at ${width}px`,
      ).toBeTruthy()

      // (1) pairwise non-overlap; a shared edge counts as clearance (rectsOverlap, layout.ts).
      for (const [a, b] of [[0, 1], [0, 2], [1, 2]] as const) {
        expect(
          rectsOverlap(m.tileBoxes[a]!, m.tileBoxes[b]!),
          `tiles ${a} and ${b} must not overlap at ${width}px (${JSON.stringify(m.tileBoxes[a])}, ${JSON.stringify(m.tileBoxes[b])})`,
        ).toBe(false)
      }

      // (2) containment in the column -- the intersection is the tile's own rect.
      for (const i of [0, 1, 2]) {
        expect(
          sameRect(overlapOf(m.tileBoxes[i]!, m.columnBox!), m.tileBoxes[i]!),
          `tile ${i} must sit wholly inside the Imported column at ${width}px (tile ${JSON.stringify(m.tileBoxes[i])}, column ${JSON.stringify(m.columnBox)})`,
        ).toBe(true)
      }

      // (3) each value's text stays inside its own tile, and fits the box it was given.
      for (const i of [0, 1, 2]) {
        const text = m.valueEdges[i]
        const tile = m.tileEdges[i]
        expect(text.outerLeft, `tile ${i}'s value must start inside its tile at ${width}px`).toBeGreaterThanOrEqual(
          tile.left - 0.5,
        )
        expect(text.outerRight, `tile ${i}'s value must end inside its tile at ${width}px`).toBeLessThanOrEqual(
          tile.right + 0.5,
        )
        expect(
          text.scrollWidth,
          `tile ${i}'s value text overflows its own box at ${width}px (${text.scrollWidth} > ${text.clientWidth})`,
        ).toBeLessThanOrEqual(text.clientWidth + 1)
      }

      fits.push({
        width,
        // Recorded, not asserted: a same-top check fails at 1280 by design.
        wrapped: m.tileBoxes[2]!.y >= m.tileBoxes[0]!.y + m.tileBoxes[0]!.height - 1,
        validSlack: m.tileEdges[0].right - m.valueEdges[0].outerRight,
        failedSlack: m.tileEdges[1].right - m.valueEdges[1].outerRight,
        notEvaluatedSlack: m.tileEdges[2].right - m.valueEdges[2].outerRight,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(fits.map((f) => f.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])
  await testInfo.attach('unvalidated-tile-geometry.json', {
    body: JSON.stringify(fits, null, 2),
    contentType: 'application/json',
  })

  // AC-6: both rows, checked only after the layout sweep above.
  const rows = page.getByTestId('review-row')
  await expect(rows, 'a document run with two unevaluated invoices renders two rows').toHaveCount(2)
  for (let i = 0; i < 2; i++) {
    await rows.nth(i).click()
    // Ties the expansion below to THIS row: row 0's identical box must be gone first.
    await expect(rows.nth(i)).toHaveAttribute('aria-expanded', 'true')
    await expect(rows.nth(1 - i)).toHaveAttribute('aria-expanded', 'false')
    await expect(page.getByTestId('review-row-expansion')).toHaveCount(1)
    await expect(page.getByTestId('review-row-not-validated')).toHaveText(
      'Not yet validated — run Re-validate to check compliance.',
    )
    await expect(page.getByTestId('review-row-passing')).toHaveCount(0)
    const expansionText = await page.getByTestId('review-row-expansion').innerText()
    // Case-insensitive: TILE_CAPTION_VALID is capitalised "Passed every rule.".
    expect(expansionText, `row ${i}'s expansion must never say passed`).not.toMatch(/passed/i)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// --- EXTR-18-07 · the deployed proof: docling's real reading, not the mock's fixed shape ---
//
// First real run is the PR deploy gate: dev-env.yml runs deployed e2e only on a non-draft PR.

test("EXTR18-E2E-01 (AC-5): the deployed reading is the document's own number", async ({ page }) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'EXTR-18-07 rich')
  const detail = await openExtractionReview(page)

  const field = detail.fields.find((f) => f.name === 'invoice_number')
  expect(field, 'no invoice_number field on the wire').toBeTruthy()
  // Equality, never a negation: a reachable-but-empty sidecar settles succeeded with the field
  // absent, and `undefined !== 'MOCK-INV-0001'` would pass on a broken extractor.
  expect(field!.value, "the settled number is not the fixture's printed number").toBe('ASC-2026-0918')
  expect(field!.reason, 'a decided field must carry reason ""').toBe('')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// A document whose invoice_number never resolves mints a QUARANTINED batch, not an invoice
// (documentCreateInput, internal/importer/document.go) -- so routeAfterRun's 'single' arm is unreachable and
// extractOneDocument's landing wait can never settle. Both fixtures below are that
// case by construction: the scan has no recoverable text at all, and the dense page's label
// OCRs as "INV0ICE NO:" (D-33). The verdict is therefore read off the deployed wire, which is
// also exactly what EXTR-15's Core AC will consume. The review SCREEN cannot serve as the
// oracle here: the review is reached from an import landing or from SourceDocumentCard, and
// both need an invoice these documents never produce -- building a surface for these terminal
// states is EXTR-15's.
async function settleOneDocument(
  page: Page,
  label: string,
  file: { name: string; buffer: Buffer },
): Promise<{ token: string; jobId: string; detail: ExtractionDetail }> {
  const token = await login(PERSONAS.A)
  const entity = await createEntity(token, { name: `${label} ${Date.now()}`, tin: freshTin() })

  await signInAs(page, 'firm', { tenantId: SHARD.a.id })
  await selectEntity(page, entity.name)

  await page.locator('header').getByRole('button', { name: 'New invoice' }).click()
  // Registered before the pick, same reason as EXTR09-E2E-01's own waiter: the upload can
  // resolve before an awaited setInputFiles returns.
  const documentPost = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname.endsWith('/api/submission/v1/documents'),
    { timeout: 120_000 },
  )
  await page
    .locator('input[type="file"]#pf-import-file')
    .setInputFiles({ name: file.name, mimeType: 'application/pdf', buffer: file.buffer })
  await page.getByRole('button', { name: 'Extract invoices' }).click()

  const documentId = ((await (await documentPost).json()) as { document_id?: string }).document_id
  expect(documentId, 'the upload must mint a stored document id').toMatch(/^[0-9a-fA-F-]{36}$/)

  let jobId = ''
  await expect
    .poll(
      async () => {
        const { jobs } = await getExtractions(token, documentId)
        const job = jobs[0]
        if (!job) return 'no job for this document yet'
        jobId = job.id
        return job.state
      },
      { message: 'the extraction never reached a terminal state', timeout: 240_000, intervals: [1_000] },
    )
    // succeeded, not dead_lettered: an unreadable TEXT layer is a reading, not a failure
    // (TestRLS_ScannedFixtureSettlesTheTextLayerUnreadable asserts the same state wired).
    .toBe('succeeded')

  // The run's own landing, pinned because it is the terminal state EXTR-15 must surface: a
  // quarantined batch still exists, so routeAfterRun returns 'review', never 'none'.
  await expect
    .poll(
      async () => {
        if (await page.getByTestId('review-table').isVisible()) return 'review batch surface'
        if (await page.getByText(/^BATCH /).first().isVisible()) return 'review batch surface'
        if (await page.getByTestId('invoice-detail').isVisible()) return 'invoice detail'
        if (await page.getByRole('button', { name: 'Extract invoices' }).isVisible()) return 'back on the picker'
        return 'nothing yet'
      },
      { message: 'a quarantined single-document run must land on the review batch surface', timeout: 120_000 },
    )
    .toBe('review batch surface')

  return { token, jobId, detail: await getExtractionDetail(token, jobId) }
}

test('EXTR18-E2E-02 (AC-8): a document with no recoverable text settles unreadable, and its pages still render', async ({
  page,
}) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const { token, jobId, detail } = await settleOneDocument(page, 'EXTR-18-07 scanned', {
    name: 'scanned_invoice.pdf',
    buffer: uniqueScannedPdfBytes(),
  })

  const textLayer = detail.fields.find((f) => f.name === 'document_text_layer')
  expect(textLayer, 'no document_text_layer field on the wire').toBeTruthy()
  expect(textLayer!.reason, 'a scan with no recoverable text must settle unreadable').toBe('unreadable')
  // worker.go's zero-text branch replaces `results` WHOLESALE, so the unreadable verdict is
  // the only row -- never one reason among ten missing fields.
  expect(
    detail.fields.map((f) => f.name),
    'the zero-text branch must settle exactly one field row',
  ).toEqual(['document_text_layer'])

  // unreadable is a TEXT verdict, not a render failure -- the distinction EXTR-15's T5 rests
  // on. Positive dimensions, not a count: a row with a 0x0 page never rendered.
  expect(detail.pages.length, 'a document with no page rows cannot prove the pages render').toBeGreaterThan(0)
  for (const p of detail.pages) {
    expect(p.width_px, `page ${p.page} rendered no width`).toBeGreaterThan(0)
    expect(p.height_px, `page ${p.page} rendered no height`).toBeGreaterThan(0)
  }

  // The stored bytes themselves, the deployed counterpart of the canvas's naturalWidth check.
  const img = await fetch(`${apiBase()}/api/submission/v1/extractions/${jobId}/pages/1`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(img.status, "page 1's image must be served").toBe(200)
  const bytes = Buffer.from(await img.arrayBuffer())
  expect(bytes.length, "page 1's image is empty").toBeGreaterThan(1_000)
  expect(bytes.subarray(0, 8).toString('hex'), 'page 1 is not a PNG').toBe('89504e470d0a1a0a')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR18-E2E-03: an image-only page the OCR can read is NOT unreadable', async ({ page }) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const { detail } = await settleOneDocument(page, 'EXTR-18-07 dense', {
    name: 'dense_invoice.pdf',
    buffer: uniqueDensePdfBytes(),
  })

  // The OCR-succeeded half of the pair: no document_text_layer row at all.
  const textLayer = detail.fields.find((f) => f.name === 'document_text_layer')
  expect(textLayer, 'an OCR-readable image page must carry no document_text_layer field').toBeUndefined()

  const total = detail.fields.find((f) => f.name === 'total')
  const currency = detail.fields.find((f) => f.name === 'currency')
  expect(total, 'no total field on the wire').toBeTruthy()
  expect(currency, 'no currency field on the wire').toBeTruthy()
  expect(total!.reason, 'total must be decided').toBe('')
  expect(currency!.reason, 'currency must be decided').toBe('')
  expect(total!.value, 'total must carry a value').not.toBeNull()
  expect(currency!.value, 'currency must carry a value').not.toBeNull()

  // Not asserted: invoice_number. OCR reads the label as "INV0ICE NO:" (digit zero for letter
  // O), the Tier-1 anchor misses, and the field settles missing (D-33) -- which is why this
  // document quarantines rather than filing, and why settleOneDocument exists.

  const lines = wireLines(detail)
  expect(lines.length, 'the OCR read fewer than 2 line rows').toBeGreaterThanOrEqual(2)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR35-E2E-01 (AC-8): the letter-spaced register files its invoice instead of quarantining', async ({
  page,
}, testInfo) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)
  const AMOUNTS = { subtotal: '14800000.00', vat: '1110000.00', total: '14430000.00' }
  const REGISTER_READ = { invoice_number: 'OAP/2026/0088', issue_date: '2026-09-01' }

  const registerName = 'advisory_register.pdf'
  const { token, entityId, jobs } = await runDocuments(page, 'Zz EXTR-35 register', [
    { name: registerName, mimeType: 'application/pdf', buffer: uniqueAdvisoryRegisterPdfBytes() },
  ])
  const job = jobs[registerName]!
  expect(job.state, `the register did not settle succeeded (kind ${job.failure_kind}, error ${job.last_error})`).toBe('succeeded')

  // Attached before any assertion: the only record of what the deployed docling read.
  const detail = await getExtractionDetail(token, job.id)
  const wire = new Map(detail.fields.map((f) => [f.name, f]))
  const read = ['invoice_number', 'issue_date', 'subtotal', 'vat', 'total'].map((k) => wire.get(k) ?? { name: k, absent: true })
  await testInfo.attach('register-fields.json', { body: JSON.stringify(read, null, 2), contentType: 'application/json' })
  const drift = 'the deployed docling read differs from local pdfium (TestAdvisory_AFreshenedRegisterStillReadsItsAmounts); see register-fields.json'
  for (const k of ['invoice_number', 'issue_date'] as const) {
    const f = wire.get(k)
    expect({ value: f?.value ?? null, reason: f?.reason ?? null }, `${k}: ${drift}`).toEqual({ value: REGISTER_READ[k], reason: '' })
  }
  for (const k of ['subtotal', 'vat', 'total'] as const) {
    const f = wire.get(k)
    expect({ value: f?.value ?? null, reason: f?.reason ?? null }, `${k}: ${drift}`).toEqual({ value: AMOUNTS[k], reason: '' })
  }

  await expect
    .poll(() => new URL(page.url()).pathname, { message: 'a filed register lands on its extraction review', timeout: 180_000 })
    .toMatch(/^\/extraction\/[0-9a-fA-F-]{36}$/)
  const exit = page.getByTestId('extraction-open-invoice')
  await expect(exit, 'the review must offer the exit to the filed invoice').toBeVisible({ timeout: 60_000 })
  await exit.click()
  await expect(page.getByTestId('invoice-detail')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(REGISTER_READ.invoice_number)

  const { invoices } = await listInvoices(token, { entity_id: entityId, limit: 50 })
  expect(invoices.map((i) => i.invoice_number), 'the fresh entity must hold exactly the filed register').toEqual([REGISTER_READ.invoice_number])
  const inv = invoices[0]
  expect((inv.issue_date ?? '').slice(0, 10)).toBe(REGISTER_READ.issue_date)
  // Money is compared at 2dp: numeric(14,2) reads a whole value back padded.
  const at2 = (v: string | null) => (v === null ? null : Number(v).toFixed(2))
  expect({ subtotal: at2(inv.subtotal), vat: at2(inv.vat), total: at2(inv.total) }).toEqual(AMOUNTS)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// These two and EXTR35-E2E-01 upload documents sharing ONE fingerprint, all as PERSONAS.A, and
// rules key on tenant+fingerprint only -- so a rule taught by one is live for every later
// upload. Order is the oracle here; deployedProofGuards.test.ts pins it.
test('EXTR36-E2E-02 (AC-3): a typed correction on a Chrome print teaches its twin', async ({
  page,
}, testInfo) => {
  // Two runDocuments calls, so twice runDocumentsIn's own 120s+240s poll budget. 600_000 is
  // under that worst case, and a timeout here retries into an already-taught tenant.
  test.setTimeout(900_000)
  const errors = collectErrors(page)

  const { token, entityId, jobs } = await runDocuments(page, 'Zz EXTR-36 chrome', [
    { name: 'chrome_register.pdf', mimeType: 'application/pdf', buffer: uniqueChromeRegisterPdfBytes() },
  ])
  const job = jobs['chrome_register.pdf']!
  expect(job.state, `the register did not settle succeeded (kind ${job.failure_kind}, error ${job.last_error})`).toBe('succeeded')

  // Attached before any assertion: the only record of what the deployed docling read.
  const detail = await getExtractionDetail(token, job.id)
  await testInfo.attach('chrome-register-detail.json', { body: JSON.stringify(detail, null, 2), contentType: 'application/json' })

  await settledInvoiceFor(token, entityId)

  // Typed, no anchor_label, no region -- a pointed payload with no region 400s
  // (CorrectionHandler, handlers_correction.go).
  const typed = await postFieldCorrection(token, job.id, 'buyer_name', { value: 'Honeywell Group Nigeria Plc', method: 'typed' })

  // A SEPARATE runDocuments call: no-duplicate-invoice-number is scoped per entity
  // (internal/importer/service.go), so the twin needs its own.
  const twin = await runDocuments(page, 'Zz EXTR-36 twin', [
    { name: 'chrome_register_twin.pdf', mimeType: 'application/pdf', buffer: uniqueChromeRegisterTwinPdfBytes() },
  ])
  const twinJob = twin.jobs['chrome_register_twin.pdf']!
  expect(twinJob.state, `the twin did not settle succeeded (kind ${twinJob.failure_kind}, error ${twinJob.last_error})`).toBe('succeeded')

  const twinDetail = await getExtractionDetail(twin.token, twinJob.id)
  await testInfo.attach('chrome-register-twin-detail.json', { body: JSON.stringify(twinDetail, null, 2), contentType: 'application/json' })

  // Primary oracle: the audit event, not the twin's reading -- the twin's buyer_name routes
  // through docling's Resolve, a different token source from this story's. The count is exact,
  // so a retry (whose first attempt already taught) reds instead of passing -- deliberate.
  const audit = await getAuditLog(token, { event: ['extraction.anchor.learned'], limit: 100 })
  // Tied to this register's invoice, not the tenant: another buyer_name learn must not stand in for this one.
  const learned = audit.events.filter((e) => {
    const p = e.payload as { field?: string; invoice_id?: string }
    return p.field === 'buyer_name' && p.invoice_id === typed.invoice_id
  })
  expect(
    learned.length,
    `recorded ${learned.length} extraction.anchor.learned event(s) for buyer_name, want exactly 1. More than one means either a retry of this spec or a re-run over a database db.Reset did not clear (it runs at gateway boot, not per workflow run). Events: ${JSON.stringify(learned.map((e) => ({ at: e.created_at, payload: e.payload })))}`,
  ).toBe(1)

  // The reason, never the value: an untaught twin reads the SAME value with reason 'ambiguous'
  // (TestRLS_AChromePrintedTwinReadsTheLearnedBuyer), so only the reason discriminates.
  const twinBuyerName = twinDetail.fields.find((f) => f.name === 'buyer_name')
  expect(
    twinBuyerName?.reason,
    `the twin's buyer_name is undecided -- the learned rule did not reach it. Field: ${JSON.stringify(twinBuyerName)}`,
  ).toBe('')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('EXTR36-E2E-01 (AC-1/AC-2): a Chrome-shaped register anchors its printed labels', async ({
  page,
}, testInfo) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)

  const { token, entityId, jobs } = await runDocuments(page, 'Zz EXTR-36 anchors', [
    { name: 'chrome_register.pdf', mimeType: 'application/pdf', buffer: uniqueChromeRegisterPdfBytes() },
  ])
  const job = jobs['chrome_register.pdf']!
  expect(job.state, `the register did not settle succeeded (kind ${job.failure_kind}, error ${job.last_error})`).toBe('succeeded')

  const detail = await getExtractionDetail(token, job.id)
  await testInfo.attach('chrome-register-anchor-detail.json', { body: JSON.stringify(detail, null, 2), contentType: 'application/json' })

  await settledInvoiceFor(token, entityId)

  // Sourced, never invented: a box matching no anchor 200s with corrected.where === null --
  // the correction applies and nothing is taught, silently.
  const buyerName = detail.fields.find((f) => f.name === 'buyer_name')
  const region = buyerName?.region
  if (!region) {
    throw new Error(
      `buyer_name carries no region on the deployed read; a pointed correction needs one. Field: ${JSON.stringify(buyerName)}`,
    )
  }

  // No anchor_label: handlers_correction.go seeds it from the client and only overrides it with
  // the server-derived label when LearnRule actually derives a rule -- sending it would make a
  // failed learn look identical to a successful one.
  await postFieldCorrection(token, job.id, 'buyer_name', {
    value: 'Honeywell Group Nigeria Plc',
    method: 'pointed',
    region,
  })

  const after = await getExtractionDetail(token, job.id)
  await testInfo.attach('chrome-register-corrected-detail.json', { body: JSON.stringify(after, null, 2), contentType: 'application/json' })
  const corrected = after.fields.find((f) => f.name === 'buyer_name')
  expect(
    corrected?.corrected?.where,
    `corrected.where did not derive a rule -- the region matched no anchor. Field: ${JSON.stringify(corrected)}`,
  ).toBe('BILLED TO')

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test("AIR03-E2E-01/02/03/04 (AC-9, AC-5, AC-6, Q1): the AI's steered reading lands beside the engine", async ({ page }) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  await extractOneDocument(page, 'AIR-03-05 steered', { name: 'ai_steered_invoice.pdf', buffer: uniqueAiSteeredPdfBytes() })

  // extractOneDocument already waited on invoice-detail, which proves the AI-only invoice
  // number filed a draft instead of quarantining (AC-6).
  const invoiceMatch = /^\/invoices\/([0-9a-fA-F-]{36})$/.exec(new URL(page.url()).pathname)
  expect(invoiceMatch, 'the steered document must land on the real invoice detail, not the quarantine').not.toBeNull()
  const invoiceId = invoiceMatch![1]

  const detail = await openExtractionReview(page)
  const wire = new Map(detail.fields.map((f) => [f.name, f]))

  // AIR03-E2E-01 (AC-9 a): the engine's own buyer_tin disagrees with the AI's Supplier-TIN
  // misread, so both render as chips -- chip 0 the wire's decided value, chip 1 the AI's own.
  const tinCell = page.getByTestId('extraction-field-buyer_tin')
  await expect(tinCell.locator('[data-testid^="extraction-chip-buyer_tin-"]'), 'buyer_tin must render exactly two chips').toHaveCount(2)
  await expect(page.getByTestId('extraction-chip-buyer_tin-0')).toHaveText('12345678-0001page 1')
  await expect(page.getByTestId('extraction-chip-buyer_tin-1')).toHaveText('87654321-0002page 1')
  await expect(tinCell.getByText('FOUND TWO POSSIBLE VALUES', { exact: true })).toBeVisible()
  const tinWire = wire.get('buyer_tin')
  expect({ value: tinWire?.value, reason: tinWire?.reason, alt: tinWire?.alternatives.map((a) => a.value) }).toEqual({
    value: '12345678-0001',
    reason: 'ambiguous',
    alt: ['87654321-0002'],
  })

  // AIR03-E2E-02 (AC-6, AC-9 b): the engine refuses the all-digit number as an amount; the AI's
  // reading, checked against the "Invoice Number:" label, decides it -- unmarked (Q12).
  const invoiceInput = page.getByTestId('extraction-input-invoice_number')
  await expect(invoiceInput).toHaveValue('20417')
  await expect(invoiceInput).toHaveJSProperty('readOnly', true)
  await expect(page.getByTestId('extraction-field-invoice_number').locator('.mono'), 'a decided field renders no pill').toHaveCount(0)
  const numberWire = wire.get('invoice_number')
  expect({ value: numberWire?.value, reason: numberWire?.reason }).toEqual({ value: '20417', reason: '' })
  expect(numberWire?.region, 'the AI-only number must still carry a region').not.toBeNull()

  // AIR03-E2E-03 (AC-5, AC-9 c): "Account Name:" fails check (c), so the missing field is
  // doubtful rather than silently filled -- the input offers the AI's text for correction.
  const nameCell = page.getByTestId('extraction-field-buyer_name')
  // pill text: REASON_PILLS
  await expect(nameCell.getByText("COULDN'T READ THIS CLEARLY", { exact: true })).toBeVisible()
  await expect(page.getByTestId('extraction-input-buyer_name')).toHaveValue('ZENITH HOLDINGS LIMITED')
  const nameWire = wire.get('buyer_name')
  expect({ value: nameWire?.value, reason: nameWire?.reason, alt: nameWire?.alternatives.map((a) => a.value) }).toEqual({
    value: null,
    reason: 'unreadable',
    alt: ['ZENITH HOLDINGS LIMITED'],
  })

  // AIR03-E2E-04 (Q1): the AI deciding invoice_number changes WHICH reading wins, never whether
  // the field is correctable here -- an unflagged locked field still answers today's 422.
  // lock note: INVOICE_NUMBER_LOCKED
  await expect(page.getByTestId('extraction-lock-invoice_number')).toHaveText(
    "The invoice number is this invoice's identity and cannot be changed here.",
  )
  const refusal = await rawFetch(`/api/submission/v1/extractions/${detail.id}/fields/invoice_number/corrections`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    body: { value: '99999', method: 'typed' },
  })
  expect(refusal.status, 'an unflagged locked field must still answer 422').toBe(422)
  // 422 sentence: msgInvoiceNumberSet
  expect(refusal.body).toEqual({ error: 'invoice_number identifies the invoice and is not corrected here' })

  const invoice = await getInvoice(token, invoiceId)
  expect({ invoice_number: invoice.invoice_number, buyer_name: invoice.buyer_name }).toEqual({
    invoice_number: '20417',
    buyer_name: null,
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// row sentence: aiUnavailableMessage (internal/importer/document.go)
const AI_UNAVAILABLE_MESSAGE =
  'AI reading was unavailable when this document was imported, so no invoice fields were taken from it. Enter this invoice manually to carry on.'
// review sentence: AI_UNAVAILABLE_REFUSAL (frontend/app/src/lib/documentRun.ts)
const AI_UNAVAILABLE_REVIEW =
  'AI reading was unavailable for this document, so there are no fields to check here. The document is still stored. Enter this invoice manually to carry on.'
// marker field: aiUnavailableField (internal/extraction/aireading.go)
const AI_UNAVAILABLE_FIELD = 'document_ai_reading'

test('AIR04-E2E-01 (AC-1, AC-3, AC-4, AC-5, AC-7): an unavailable AI sends the document to manual entry with no reading', async ({
  page,
}) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)
  const name = 'ai_unavailable_invoice.pdf'

  // (a) AC-1. Zz sorts this entity after the existing ones (entity-ordering trap).
  const { token, documentIds, jobs } = await runDocuments(page, 'Zz AIR-04 unavailable', [
    { name, mimeType: 'application/pdf', buffer: uniqueAiUnavailablePdfBytes() },
  ])
  const documentId = documentIds[name]!
  const job = jobs[name]!
  expect(job.state, `did not settle succeeded (kind ${job.failure_kind}, error ${job.last_error})`).toBe('succeeded')
  expect(job.failure_kind).toBeNull()

  // (b) AC-1: one marker-only row, nothing engine-read reaches the wire.
  const detail = await getExtractionDetail(token, job.id)
  expect(
    detail.fields.map((f) => ({ name: f.name, value: f.value, reason: f.reason, alternatives: f.alternatives.length })),
  ).toEqual([{ name: AI_UNAVAILABLE_FIELD, value: null, reason: 'unreadable', alternatives: 0 }])

  // (c) AC-4: the document quarantines to the review batch surface, its own sentence.
  await expect
    .poll(() => new URL(page.url()).pathname, { timeout: 180_000 })
    .toMatch(/^\/imports\/[0-9a-fA-F-]{36}\/review$/)
  const tab = page.getByRole('button', { name: 'Quarantined documents (1)', exact: true })
  await expect(tab).toHaveCount(1)
  await tab.click()
  const row = page.getByTestId('unreadable-row')
  await expect(row).toHaveCount(1)
  await expect(row).toContainText(name)
  await expect(row).toContainText(AI_UNAVAILABLE_MESSAGE)
  await expect(row).not.toContainText('was read, but no invoice number') // noInvoiceNumberMessage
  await expect(row).not.toContainText('too poor to read') // poorScanMessage

  // (d) AC-5: floor first (buttons render on this tab), then the absent affordance.
  const handOff = row.getByRole('button', { name: 'Enter it by hand' })
  await expect(handOff).toBeEnabled()
  await expect(page.getByRole('button', { name: /read.*again/i })).toHaveCount(0)

  // (e) AC-3, AC-7: no carried reading, either through the API or the carried form.
  expect(await getCarriedReading(token, documentId)).toBeNull()
  const readingGet = page.waitForResponse(
    (r) => r.request().method() === 'GET' && new URL(r.url()).pathname.endsWith('/api/invoice/v1/imports/document/reading'),
    { timeout: 60_000 },
  )
  await handOff.click()
  const readingRes = await readingGet
  expect(readingRes.status()).toBe(200)
  expect(((await readingRes.json()) as { reading: unknown }).reading).toBeNull()
  await expect(page.getByRole('button', { name: 'Invoice number is required' })).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText(CARRIED_CAPTION)).toHaveCount(0)
  await expect(page.locator('[data-testid^="carried-"]')).toHaveCount(0)

  // (f) the blank hand-off form still files, and attaches the same document.
  const invoiceNumber = `AIR04-${Date.now()}`
  const invoiceId = await fileHandOffDraft(page, invoiceNumber)
  const source = await readSourceDocument(token, invoiceId)
  expect(source.document?.id).toBe(documentId)

  // (g) AC-4, AC-5, AC-7. Not openExtractionReview(): it asserts extraction-page-1, which this
  // screen must never render. Inlined the same click/waitForResponse pattern.
  const control = page.getByTestId('open-extraction-review')
  await expect(control).toBeEnabled({ timeout: 60_000 })
  const [reviewRes] = await Promise.all([
    page.waitForResponse(
      (r) =>
        r.request().method() === 'GET' &&
        /\/api\/submission\/v1\/extractions\/[0-9a-fA-F-]{36}$/.test(new URL(r.url()).pathname),
      { timeout: 120_000 },
    ),
    control.click(),
  ])
  expect(reviewRes.status()).toBe(200)
  await expect(page.getByTestId('extraction-review')).toBeVisible({ timeout: 60_000 })
  await expect(page.getByText(AI_UNAVAILABLE_REVIEW, { exact: true })).toBeVisible()
  await expect(page.getByTestId('extraction-review-body')).toHaveCount(0)
  await expect(page.getByTestId('extraction-save')).toHaveCount(0)
  await expect(page.getByTestId('extraction-canvas')).toHaveCount(0)
  await expect(page.getByTestId('extraction-open-invoice')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /read.*again/i })).toHaveCount(0)

  // (h)
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// pill: ExtractionFields.tsx's NO_REGION
const NO_REGION_PILL = 'NO REGION'
// note: ExtractionCanvas.tsx's NO_REGION
const NO_REGION_NOTE = 'We have no region for this field, so there is nothing to highlight.'

test('AIR05-E2E-01 (AC-3, AC-4, AC-5, AC-8): a document with no text is read from its page images', async ({
  page,
}) => {
  test.setTimeout(600_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  // (a) AC-5, AC-8. Zz sorts this entity after the existing ones (entity-ordering trap).
  // extractOneDocument already waited on invoice-detail, which proves the answered image read
  // filed a draft instead of quarantining.
  await extractOneDocument(page, 'Zz AIR-05 image read', { name: 'scanned_invoice.pdf', buffer: uniqueImageSteeredPdfBytes() })
  const invoiceMatch = /^\/invoices\/([0-9a-fA-F-]{36})$/.exec(new URL(page.url()).pathname)
  expect(invoiceMatch, 'an answered image read must land on the real invoice detail, not the quarantine').not.toBeNull()
  const invoiceId = invoiceMatch![1]

  // (b) AC-3, AC-4: no document_text_layer field reaches the wire once the image read answers.
  const detail = await openExtractionReview(page)
  const wire = new Map(detail.fields.map((f) => [f.name, f]))
  expect(wire.has('document_text_layer'), 'the image read must replace the text-layer verdict').toBe(false)

  const invoiceNumberWire = wire.get('invoice_number')
  expect({
    value: invoiceNumberWire?.value,
    reason: invoiceNumberWire?.reason,
    region: invoiceNumberWire?.region,
    alternatives: invoiceNumberWire?.alternatives,
  }).toEqual({ value: 'INV-5520', reason: '', region: null, alternatives: [] })

  const totalWire = wire.get('total')
  expect({
    value: totalWire?.value,
    reason: totalWire?.reason,
    region: totalWire?.region,
    alternatives: totalWire?.alternatives,
  }).toEqual({ value: '1935.00', reason: '', region: null, alternatives: [] })

  const buyerTinWire = wire.get('buyer_tin')
  expect({ value: buyerTinWire?.value, reason: buyerTinWire?.reason }).toEqual({ value: null, reason: 'unreadable' })
  expect(buyerTinWire?.alternatives).toEqual([{ value: '9999999-1202', region: null }])

  // (c) AC-3, AC-8: a decided field with no region still renders, with the NO REGION pill.
  await expect(page.getByTestId('extraction-input-invoice_number')).toHaveValue('INV-5520')
  await expect(page.getByTestId('extraction-field-invoice_number').getByText(NO_REGION_PILL, { exact: true })).toBeVisible()

  // (d) AC-3: selecting that field shows the canvas' no-region note, not a highlight.
  await page.getByTestId('extraction-field-invoice_number').click()
  await expect(page.getByTestId('extraction-no-region')).toHaveText(NO_REGION_NOTE)

  // (e) AC-4: the doubtful buyer_tin offers the AI's own reading for correction.
  // pill text: REASON_PILLS
  await expect(page.getByTestId('extraction-field-buyer_tin').getByText("COULDN'T READ THIS CLEARLY", { exact: true })).toBeVisible()
  await expect(page.getByTestId('extraction-input-buyer_tin')).toHaveValue('9999999-1202')

  // (f) AC-3, Q12: an AI-only reading is filed with no marking anywhere in the review copy.
  const reviewText = await page.getByTestId('extraction-review').innerText()
  expect(reviewText).not.toMatch(/\bAI\b/)
  expect(reviewText).not.toMatch(/artificial/i)

  // (g) AC-4, D2: a value that failed its format check is never decided onto the invoice.
  const invoice = await getInvoice(token, invoiceId)
  expect({ invoice_number: invoice.invoice_number, total: invoice.total, buyer_tin: invoice.buyer_tin }).toEqual({
    invoice_number: 'INV-5520',
    total: '1935.00',
    buyer_tin: null,
  })

  // (h)
  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// `${value}page ${region.page}` matches LineItemGrid's own chip render (CHIP_VALUE span
// immediately followed by CHIP_WHERE, no separator) -- the same convention AIR03-E2E-01 reads
// off ExtractionFields' chips. Never hardcodes whether a region is present: it reads that off
// the wire response itself, never off a literal copied from the fixture builder.
function lineChipText(value: string, region: ExtractionRegion | null): string {
  return region === null ? value : `${value}page ${region.page}`
}

test('AIR08-E2E-01/02/03/04 (Core AC 4, 5, 6, 8): the AI reads line items once, and a disagreement reaches the grid', async ({
  page,
}) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  // AIR08-E2E-01 (AC-4): extractOneDocument already waited on invoice-detail, which proves the
  // steered document filed a draft instead of quarantining.
  await extractOneDocument(page, 'Zz AIR-08 lines', { name: 'ai_lines_invoice.pdf', buffer: uniqueAiLinesPdfBytes() })
  const invoiceMatch = /^\/invoices\/([0-9a-fA-F-]{36})$/.exec(new URL(page.url()).pathname)
  expect(invoiceMatch, 'the steered document must land on the real invoice detail, not the quarantine').not.toBeNull()

  const detail = await openExtractionReview(page)
  const lines = wireLines(detail)
  expect(
    lines.map((l) => l.index),
    'the extraction detail must carry three contiguous line indices',
  ).toEqual([1, 2, 3])
  const fieldsByName = new Map(detail.fields.map((f) => [f.name, f]))

  await expect(page.getByTestId('line-item-grid'), 'the fields pane rendered no line-item grid').toBeVisible({
    timeout: 30_000,
  })
  await expect(page.locator('[data-testid^="line-item-row-"]'), 'the grid did not render one row per wire line').toHaveCount(3)

  // AIR08-E2E-02 (AC-5, agreeing row): row 1's four cells render the engine's own values in
  // ordinary inputs, with no chip and no pill anywhere on that row.
  const row1 = lines[0]
  const row1Want: Record<LineRoleName, string> = { description: 'Widget', quantity: '2', unit_price: '500.00', line_total: '1000.00' }
  for (const role of LINE_ROLE_NAMES) {
    expect(row1.cells[role]?.value, `row 1's ${role} did not carry the engine's own reading on the wire`).toBe(row1Want[role])
    expect(fieldsByName.get(row1.cells[role]?.name ?? '')?.reason, `row 1's ${role} is not decided on the wire`).toBe('')
    await expect(
      page.getByTestId(`line-item-input-1-${role}`),
      `row 1's ${role} input did not carry the engine's own reading`,
    ).toHaveValue(row1Want[role])
  }
  await expect(page.locator('[data-testid^="line-item-chip-1-"]'), 'the agreeing row renders a chip').toHaveCount(0)
  await expect(page.getByTestId('line-item-ambiguous-1'), 'the agreeing row renders an ambiguous pill').toHaveCount(0)
  await expect(page.getByTestId('line-item-flag-1'), 'the agreeing row renders an arithmetic pill').toHaveCount(0)

  // AIR08-E2E-04 (Core AC 6, disagreement): row 2's unit_price renders two chips, chip 0 the
  // engine's own value (with its wire region, if any), chip 1 the AI's with none; the other
  // three cells of the same row stay ordinary inputs at the engine's own reading.
  const row2 = lines[1]
  const upWire = row2.cells.unit_price as WireLineCell
  expect(
    { value: upWire.value, reason: fieldsByName.get(upWire.name)?.reason },
    'row 2 unit_price must be the engine reading, ambiguous',
  ).toEqual({ value: '250.00', reason: 'ambiguous' })
  const upAlternatives: ExtractionCandidate[] = fieldsByName.get(upWire.name)?.alternatives ?? []
  expect(upAlternatives, 'row 2 unit_price must carry exactly one alternative, the AI\'s own').toEqual([{ value: '260.00', region: null }])

  const upChips = page.locator('[data-testid^="line-item-chip-2-unit_price-"]')
  await expect(upChips, 'row 2 unit_price must render exactly two chips').toHaveCount(2)
  await expect(page.getByTestId('line-item-chip-2-unit_price-0')).toHaveText(lineChipText('250.00', upWire.region))
  await expect(page.getByTestId('line-item-chip-2-unit_price-1')).toHaveText(lineChipText('260.00', null))
  await expect(
    page.getByTestId('line-item-ambiguous-2'),
    'row 2 must show the FOUND TWO POSSIBLE VALUES pill',
  ).toHaveText('FOUND TWO POSSIBLE VALUES')
  await expect(page.getByTestId('line-item-flag-2'), 'row 2 must not also carry an arithmetic pill').toHaveCount(0)

  const row2OtherWant: Partial<Record<LineRoleName, string>> = { description: 'Gadget', quantity: '3', line_total: '750.00' }
  for (const role of ['description', 'quantity', 'line_total'] as const) {
    expect(row2.cells[role]?.value, `row 2's ${role} did not carry the engine's own reading on the wire`).toBe(row2OtherWant[role])
    expect(fieldsByName.get(row2.cells[role]?.name ?? '')?.reason, `row 2's ${role} was disturbed by the unit_price disagreement`).toBe('')
    await expect(
      page.getByTestId(`line-item-input-2-${role}`),
      `row 2's ${role} input did not carry the engine's own reading`,
    ).toHaveValue(row2OtherWant[role] as string)
  }

  // AIR08-E2E-03 (Core AC 8, AI-only row): row 3 renders the AI's own values in ordinary
  // inputs, its wire cells carry reason '' and empty alternatives, and the row shows no pill --
  // UNMARKED (Q12).
  const row3 = lines[2]
  const row3Want: Record<LineRoleName, string> = { description: 'Delivery', quantity: '1', unit_price: '90.00', line_total: '90.00' }
  for (const role of LINE_ROLE_NAMES) {
    const cell = row3.cells[role] as WireLineCell
    expect(cell.value, `row 3's ${role} did not carry the AI's own reading on the wire`).toBe(row3Want[role])
    expect(fieldsByName.get(cell.name)?.reason, `row 3's ${role} is not unmarked on the wire`).toBe('')
    const alternatives: ExtractionCandidate[] = fieldsByName.get(cell.name)?.alternatives ?? []
    expect(alternatives, `row 3's ${role} carries an alternative -- AI-only rows are unmarked`).toEqual([])
    await expect(
      page.getByTestId(`line-item-input-3-${role}`),
      `row 3's ${role} input did not carry the AI's own reading`,
    ).toHaveValue(row3Want[role])
  }
  await expect(page.locator('[data-testid^="line-item-chip-3-"]'), 'the AI-only row renders a chip').toHaveCount(0)
  await expect(page.getByTestId('line-item-ambiguous-3'), 'the AI-only row renders an ambiguous pill').toHaveCount(0)
  await expect(page.getByTestId('line-item-flag-3'), 'the AI-only row renders an arithmetic pill').toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test('AIR08-LAYOUT-01: with the disagreement chip row rendered, the grid scrollbox stays inside the fields pane body at every width', async ({
  page,
}, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'Zz AIR-08 layout', { name: 'ai_lines_invoice.pdf', buffer: uniqueAiLinesPdfBytes() })
  await openExtractionReview(page)
  await expect(page.getByTestId('line-item-grid'), 'the fields pane rendered no line-item grid').toBeVisible({
    timeout: 30_000,
  })

  // Non-empty floor: the sweep below measures the chip row, so it must actually be on screen.
  await expect(
    page.locator('[data-testid^="line-item-chip-2-unit_price-"]'),
    'row 2 renders no chip row -- the sweep below would measure an input row instead',
  ).toHaveCount(2)

  const paneBody = fieldsPaneBody(page)
  const scroll = page.getByTestId('line-item-scroll')

  const measured: { width: number; left: number; right: number; bodyScrollWidth: number; bodyClientWidth: number }[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first, WIDE_WIDTHS' own order.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [s, b] = await Promise.all([scroll.boundingBox(), paneBody.boundingBox()])
        const flow = await paneBody.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        return { s, b, flow }
      }, `line grid containment (chip row) at ${width}px`)

      expect(m.s && m.b, `both the scrollbox and the pane body must render at ${width}px`).toBeTruthy()
      // Non-empty first: a rect collapsed to zero is inside anything and passes vacuously.
      expect(m.s!.width, `the scrollbox collapsed to zero width at ${width}px -- its edges are vacuous`).toBeGreaterThan(0)
      expect(m.b!.width, `the pane body collapsed to zero width at ${width}px`).toBeGreaterThan(0)

      const g = gaps(m.s as Rect, m.b as Rect)
      expect(g.left, `the scrollbox starts ${(-g.left).toFixed(1)}px left of the pane body at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the scrollbox ends ${(-g.right).toFixed(1)}px right of the pane body at ${width}px`).toBeGreaterThanOrEqual(-1)

      expect(
        m.flow.scrollWidth,
        `the pane body holds ${m.flow.scrollWidth}px of content in a ${m.flow.clientWidth}px box at ${width}px -- the chip row's overflow escaped its scrollbox`,
      ).toBeLessThanOrEqual(m.flow.clientWidth + 1)

      measured.push({
        width,
        left: g.left,
        right: g.right,
        bodyScrollWidth: m.flow.scrollWidth,
        bodyClientWidth: m.flow.clientWidth,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  await testInfo.attach('extraction-line-grid-chip-containment.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// read: fxJevDoubtLines (internal/extraction/fixtures_test.go)
const JEV_DOUBT_READ = { invoice_number: 'JD-3310', supplier_name: 'Kaduna Textiles Limited', supplier_tin: '23456789-0001' }

test('CHECK03-E2E-01 (AC-4, AC-5, AC-10): a value Jev doubts shows the pill, keeps its value, and files', async ({ page }) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  // extractOneDocument already waited on invoice-detail: the doubted number still filed a draft.
  await extractOneDocument(page, 'Zz CHECK-03 doubt', { name: 'jev_doubt_invoice.pdf', buffer: uniqueJevDoubtPdfBytes() })
  const invoiceMatch = /^\/invoices\/([0-9a-fA-F-]{36})$/.exec(new URL(page.url()).pathname)
  expect(invoiceMatch, 'the doubted document must land on the real invoice detail, not the quarantine').not.toBeNull()
  const invoiceId = invoiceMatch![1]

  const detail = await openExtractionReview(page)
  const wire = new Map(detail.fields.map((f) => [f.name, f]))

  // The doubt flags the value and keeps it, and a flagged number drops its lock.
  const numberCell = page.getByTestId('extraction-field-invoice_number')
  await expect(numberCell.getByText(REASON_PILL.unreadable, { exact: true })).toBeVisible()
  const numberInput = page.getByTestId('extraction-input-invoice_number')
  await expect(numberInput).toHaveValue(JEV_DOUBT_READ.invoice_number)
  await expect(numberInput).toHaveJSProperty('readOnly', false)
  expect(await numberInput.getAttribute('aria-readonly'), 'a doubted invoice number still announces itself read-only').not.toBe('true')
  await expect(page.getByTestId('extraction-lock-invoice_number')).toHaveCount(0)
  // lock note: INVOICE_NUMBER_LOCKED
  await expect(
    numberCell.getByText("The invoice number is this invoice's identity and cannot be changed here.", { exact: true }),
  ).toHaveCount(0)
  const numberWire = wire.get('invoice_number')
  expect({ value: numberWire?.value, reason: numberWire?.reason }).toEqual({
    value: JEV_DOUBT_READ.invoice_number,
    reason: 'unreadable',
  })

  // The supplier pair is never asked, so it stays decided with no pill.
  for (const name of ['supplier_name', 'supplier_tin'] as const) {
    const w = wire.get(name)
    expect({ value: w?.value, reason: w?.reason }, `${name} on the wire`).toEqual({ value: JEV_DOUBT_READ[name], reason: '' })
    await expect(page.getByTestId(`extraction-field-${name}`).locator('.mono'), `${name} renders a pill`).toHaveCount(0)
  }

  // The other seven header fields were never read, so never asked.
  const decided: string[] = Object.keys(JEV_DOUBT_READ)
  for (const name of VOCABULARY.filter((n) => !decided.includes(n))) {
    const w = wire.get(name)
    expect({ value: w?.value, reason: w?.reason }, `${name} on the wire`).toEqual({ value: null, reason: 'missing' })
  }

  // Floor: the one-pill count below is vacuous over a pane that dropped cells.
  const cells = page.locator('[data-testid^="extraction-field-"]')
  await expect(cells, 'the pane does not render one cell per header field').toHaveCount(VOCABULARY.length)
  const pilled = cells.filter({ hasText: REASON_PILL.unreadable })
  await expect(pilled, 'a cell other than invoice_number renders the doubt pill').toHaveCount(1)
  await expect(pilled).toHaveAttribute('data-testid', 'extraction-field-invoice_number')

  // marker prefix: jev fake.go's markers (fxJevDoubtMarker). Docling must not read it into a value.
  const leaked = detail.fields.flatMap((f) =>
    [f.value, ...f.alternatives.map((a) => a.value)].filter((v) => v?.includes('JEVFAKE')).map((v) => `${f.name}=${v}`),
  )
  expect(leaked, 'the Jev marker reached a wire value').toEqual([])

  const invoice = await getInvoice(token, invoiceId)
  expect(invoice.invoice_number).toBe(JEV_DOUBT_READ.invoice_number)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

test("CHECK03-LAYOUT-01: the doubted invoice number's pill stays inside its cell at every width", async ({ page }, testInfo) => {
  test.setTimeout(300_000)
  const errors = collectErrors(page)

  await extractOneDocument(page, 'Zz CHECK-03 layout', { name: 'jev_doubt_invoice.pdf', buffer: uniqueJevDoubtPdfBytes() })
  await openExtractionReview(page)

  const pane = page.getByTestId('extraction-fields')
  const paneBody = fieldsPaneBody(page)
  const cell = page.getByTestId('extraction-field-invoice_number')
  const pill = cell.getByText(REASON_PILL.unreadable, { exact: true })

  // Non-empty floor: the sweep below measures the pill, so it must actually be on screen.
  await expect(pill, 'invoice_number renders no doubt pill -- the sweep below would measure nothing').toBeVisible({
    timeout: 30_000,
  })

  const measured: { width: number; left: number; right: number; bodyScrollWidth: number; bodyClientWidth: number }[] = []
  const entryViewport = page.viewportSize()
  try {
    // Widest first, WIDE_WIDTHS' own order.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [p, c] = await Promise.all([pill.boundingBox(), cell.boundingBox()])
        const flow = await paneBody.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        return { p, c, flow }
      }, `doubt pill containment at ${width}px`)

      expect(m.p && m.c, `both the pill and its cell must render at ${width}px`).toBeTruthy()
      // Non-empty first: a rect collapsed to zero is inside anything and passes vacuously.
      expect(m.p!.width, `the pill collapsed to zero width at ${width}px`).toBeGreaterThan(0)

      const g = gaps(m.p as Rect, m.c as Rect)
      expect(g.left, `the pill starts ${(-g.left).toFixed(1)}px left of its cell at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the pill ends ${(-g.right).toFixed(1)}px right of its cell at ${width}px`).toBeGreaterThanOrEqual(-1)

      expect(
        m.flow.scrollWidth,
        `the pane body holds ${m.flow.scrollWidth}px of content in a ${m.flow.clientWidth}px box at ${width}px`,
      ).toBeLessThanOrEqual(m.flow.clientWidth + 1)

      measured.push({
        width,
        left: g.left,
        right: g.right,
        bodyScrollWidth: m.flow.scrollWidth,
        bodyClientWidth: m.flow.clientWidth,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])

  // The pane reaches its floor at no WIDE_WIDTHS entry, so descend until it does.
  try {
    const { floorWidth, descent } = await descendToPaneFloor(page, pane)
    await testInfo.attach('check03-doubt-pill-descent.json', {
      body: JSON.stringify({ wide: measured, descent, floorWidth }, null, 2),
      contentType: 'application/json',
    })

    const spill = await cell.evaluate((el) => {
      const c = el.getBoundingClientRect()
      let worst = { node: '', outLeft: 0, outRight: 0 }
      for (const d of Array.from(el.querySelectorAll<HTMLElement>('*'))) {
        const r = d.getBoundingClientRect()
        // A rect collapsed on both axes is inside anything and would pass vacuously.
        if (r.width === 0 && r.height === 0) continue
        const outLeft = c.left - r.left
        const outRight = r.right - c.right
        if (Math.max(outLeft, outRight) > Math.max(worst.outLeft, worst.outRight)) {
          worst = { node: d.dataset.testid ?? d.tagName.toLowerCase(), outLeft, outRight }
        }
      }
      return { scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, worst }
    })

    await testInfo.attach('check03-doubt-pill-floor.json', {
      body: JSON.stringify({ floorWidth, spill }, null, 2),
      contentType: 'application/json',
    })

    expect(spill.clientWidth, `invoice_number has no width at the ${floorWidth}px floor -- its edges are vacuous`).toBeGreaterThan(0)
    expect(
      spill.worst.outLeft,
      `${spill.worst.node} starts ${spill.worst.outLeft.toFixed(2)}px left of the invoice_number cell at ${floorWidth}px`,
    ).toBeLessThanOrEqual(1)
    expect(
      spill.worst.outRight,
      `${spill.worst.node} ends ${spill.worst.outRight.toFixed(2)}px right of the invoice_number cell at ${floorWidth}px`,
    ).toBeLessThanOrEqual(1)
    expect(
      spill.scrollWidth,
      `invoice_number holds ${spill.scrollWidth}px of content in a ${spill.clientWidth}px box at ${floorWidth}px`,
    ).toBeLessThanOrEqual(spill.clientWidth + 1)
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

// read: fxJevReceiptLines (internal/extraction/fixtures_test.go)
const JEV_RECEIPT_READ = { invoice_number: 'JR-4410', supplier_name: 'Owerri Traders Limited', supplier_tin: '34567890-0001' }
// copy: DOCUMENT_TYPE_NOTICE.receipt (frontend/app/src/lib/extractionReview.ts)
const RECEIPT_NOTICE =
  'This looks like a receipt, not a tax invoice. Its import was not changed. Check it before you submit an invoice from it.'

test('CHECK04-E2E-01 (AC-4, AC-5, AC-8): a document read as a receipt shows the banner, keeps its rows, and files; a plain upload shows none', async ({ page }, testInfo) => {
  // Two extractions on a possibly cold sidecar, then a four-width sweep.
  test.setTimeout(600_000)
  const errors = collectErrors(page)
  const token = await login(PERSONAS.A)

  // -- receipt leg -- extractOneDocument already waited on invoice-detail: the receipt filed a draft.
  await extractOneDocument(page, 'Zz CHECK-04 receipt', { name: 'jev_receipt_invoice.pdf', buffer: uniqueJevReceiptPdfBytes() })
  const invoiceMatch = /^\/invoices\/([0-9a-fA-F-]{36})$/.exec(new URL(page.url()).pathname)
  expect(invoiceMatch, 'the receipt must land on the real invoice detail, not the quarantine').not.toBeNull()
  const invoiceId = invoiceMatch![1]

  const detail = await openExtractionReview(page)
  expect(detail.document_type, 'the wire carries no receipt verdict').toBe('receipt')
  const strip = page.getByTestId('extraction-document-type')
  await expect(strip).toBeVisible()
  await expect(strip).toHaveText(RECEIPT_NOTICE)

  await expect(page.locator('[data-testid^="extraction-field-"]'), 'the pane does not render one cell per header field').toHaveCount(
    VOCABULARY.length,
  )
  // The verdict moves no row: the three printed values stay decided with no pill.
  const wire = new Map(detail.fields.map((f) => [f.name, f]))
  for (const name of Object.keys(JEV_RECEIPT_READ) as (keyof typeof JEV_RECEIPT_READ)[]) {
    const w = wire.get(name)
    expect({ value: w?.value, reason: w?.reason }, `${name} on the wire`).toEqual({ value: JEV_RECEIPT_READ[name], reason: '' })
    await expect(page.getByTestId(`extraction-field-${name}`).locator('.mono'), `${name} renders a pill`).toHaveCount(0)
  }
  const decided: string[] = Object.keys(JEV_RECEIPT_READ)
  for (const name of VOCABULARY.filter((n) => !decided.includes(n))) {
    const w = wire.get(name)
    expect({ value: w?.value, reason: w?.reason }, `${name} on the wire`).toEqual({ value: null, reason: 'missing' })
  }
  expect(
    detail.fields.map((f) => f.name),
    'the verdict became a field row',
  ).not.toContain('document_type')

  // marker prefix: jev fake.go's markers (fxJevReceiptMarker). Docling must not read it into a value.
  const leaked = detail.fields.flatMap((f) =>
    [f.value, ...f.alternatives.map((a) => a.value)].filter((v) => v?.includes('JEVFAKE')).map((v) => `${f.name}=${v}`),
  )
  expect(leaked, 'the Jev marker reached a wire value').toEqual([])

  const invoice = await getInvoice(token, invoiceId)
  expect(invoice.invoice_number).toBe(JEV_RECEIPT_READ.invoice_number)

  // The strip wraps inside the review and leaves the body room below it, at every width.
  const review = page.getByTestId('extraction-review')
  const body = page.getByTestId('extraction-review-body')
  const measured: { width: number; left: number; right: number; scrollWidth: number; clientWidth: number; bodyHeight: number }[] =
    []
  const entryViewport = page.viewportSize()
  try {
    // Widest first, WIDE_WIDTHS' own order.
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const m = await settledRead(async () => {
        const [s, r, b] = await Promise.all([strip.boundingBox(), review.boundingBox(), body.boundingBox()])
        const flow = await strip.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        return { s, r, b, flow }
      }, `document-type strip containment at ${width}px`)

      expect(m.s && m.r && m.b, `the strip, the review and its body must render at ${width}px`).toBeTruthy()
      // Non-empty first: a rect collapsed to zero is inside anything and passes vacuously.
      expect(m.s!.width, `the strip collapsed to zero width at ${width}px`).toBeGreaterThan(0)

      const g = gaps(m.s as Rect, m.r as Rect)
      expect(g.left, `the strip starts ${(-g.left).toFixed(1)}px left of the review at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(g.right, `the strip ends ${(-g.right).toFixed(1)}px right of the review at ${width}px`).toBeGreaterThanOrEqual(-1)
      expect(
        m.flow.scrollWidth,
        `the strip holds ${m.flow.scrollWidth}px of content in a ${m.flow.clientWidth}px box at ${width}px`,
      ).toBeLessThanOrEqual(m.flow.clientWidth + 1)
      expect(m.b!.height, `the review body has no height below the strip at ${width}px`).toBeGreaterThan(0)
      expect(m.b!.y, `the review body starts above the strip's bottom at ${width}px`).toBeGreaterThanOrEqual(
        m.s!.y + m.s!.height - 1,
      )

      measured.push({
        width,
        left: g.left,
        right: g.right,
        scrollWidth: m.flow.scrollWidth,
        clientWidth: m.flow.clientWidth,
        bodyHeight: m.b!.height,
      })
    }
  } finally {
    if (entryViewport) await page.setViewportSize(entryViewport)
  }

  expect(measured.map((m) => m.width), 'every WIDE_WIDTHS entry must be measured, widest first').toEqual([...WIDE_WIDTHS])
  await testInfo.attach('check04-document-type-strip.json', {
    body: JSON.stringify(measured, null, 2),
    contentType: 'application/json',
  })

  // -- plain leg -- the fake's default answer is a tax invoice, so no verdict and no strip.
  await extractOneDocument(page, 'Zz CHECK-04 plain')
  const plain = await openExtractionReview(page)
  expect(plain.document_type, 'a plain upload carries a verdict').toBeNull()
  await expect(page.getByTestId('extraction-document-type')).toHaveCount(0)

  expect(errors, `console errors on the app:\n${errors.join('\n')}`).toEqual([])
})

