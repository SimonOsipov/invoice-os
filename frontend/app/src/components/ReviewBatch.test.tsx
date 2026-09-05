// @vitest-environment jsdom
// APPR-16-01, task-534 -- runtime render coverage the Mode A red phase didn't provide.
// LIB-SCAN-1/A16-1c only see SOURCE text; they cannot see a string composed at runtime
// (e.g. a caption reassembled from parts, or a stray literal reintroduced beside the
// exported constant without the source scan noticing). This renders the real
// ReviewBatch component end to end and asserts on what actually lands in the DOM.
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { ImportBatch } from '../lib/importApi'
import { reviewFooterSummary, TILE_CAPTION_VALID } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { ReviewBatch } from './ReviewBatch'

afterEach(cleanup)

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function listResponse(total: number): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve({ invoices: [], pagination: { limit: 1, offset: 0, total } }) }
}

const TOTALS = { allTotal: 500, cleanTotal: 474, queuedTotal: 6, failingTotal: 20, keptTotal: 3 }

function batch(over: Partial<ImportBatch> = {}): ImportBatch {
  return {
    id: 'b1',
    entity_id: 'ent-1',
    filename: 'june.csv',
    status: 'completed',
    rows_total: 500,
    rows_valid: 480,
    rows_invalid: 20,
    errors: [],
    rule_set_version: 3,
    created_at: '2026-08-01T00:00:00Z',
    ...over,
  }
}

// The shell fires six concurrent GETs (batch + four pill counts + kept-as-is), and the
// invoices tab fires two more (its own paginated list + violation summary) -- dispatched
// by URL/param, not call order, mirroring InvoiceDetail.test.tsx's mockDetailFetch idiom.
function mockReviewFetch(b: ImportBatch, totals: typeof TOTALS) {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes('/imports/')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(b) })
    }
    if (url.includes('/violation-summary')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve({ rules: [] }) })
    }
    if (url.includes('/invoices')) {
      const params = new URL(url).searchParams
      if (params.get('kept_as_is') === 'true') return Promise.resolve(listResponse(totals.keptTotal))
      if (params.get('needs_fix') === 'true') return Promise.resolve(listResponse(totals.failingTotal))
      if (params.get('status') === 'validated') return Promise.resolve(listResponse(totals.cleanTotal))
      if (params.get('status') === 'queued') return Promise.resolve(listResponse(totals.queuedTotal))
      if (params.get('limit') === '1') return Promise.resolve(listResponse(totals.allTotal))
      // The invoices tab's own paginated list (not one of the shell's limit:1 pill counts).
      return Promise.resolve(listResponse(0))
    }
    return Promise.reject(new Error(`unmocked url: ${url}`))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function reviewCtx(batchIds: string[]): PlatformCtx {
  const ctx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    reviewBatchIds: batchIds,
    run: null,
    restartImport: vi.fn(),
    openImportedInvoice: vi.fn(),
    closeCreate: vi.fn(),
    skipUpload: vi.fn(),
  }
  return ctx as unknown as PlatformCtx
}

describe('ReviewBatch: rendered captions name validation, not entitlement (APPR-16-01)', () => {
  it('R16-1: the tile caption rendered in the DOM is exactly TILE_CAPTION_VALID, not an inline literal', async () => {
    mockReviewFetch(batch(), TOTALS)

    render(<ReviewBatch ctx={reviewCtx(['b1'])} />)

    await waitFor(() => expect(screen.getByText(TILE_CAPTION_VALID)).toBeTruthy())
    // A wired-up assertion, not just an exported-constant check: nothing on the page
    // says the old inline literal, and the ONLY occurrence of the caption text matches
    // the export byte for byte (getByText throws on more than one match).
    expect(document.body.textContent).toContain('Passed every rule.')
  })

  it('R16-2: the rendered footer line is exactly reviewFooterSummary(totals)', async () => {
    mockReviewFetch(batch(), TOTALS)

    render(<ReviewBatch ctx={reviewCtx(['b1'])} />)

    const expected = reviewFooterSummary(TOTALS)
    await waitFor(() => expect(screen.getByText(expected)).toBeTruthy())
  })

  it('R16-3: no rendered text on the screen contains "ready to submit", in any case', async () => {
    mockReviewFetch(batch(), TOTALS)

    render(<ReviewBatch ctx={reviewCtx(['b1'])} />)

    await waitFor(() => expect(screen.getByText(TILE_CAPTION_VALID)).toBeTruthy())
    // The source scan (A16-1c) can only see ReviewBatch.tsx's own text; this catches a
    // string composed at runtime (e.g. concatenated from parts) that a source scan
    // structurally cannot.
    expect(document.body.textContent?.toLowerCase()).not.toContain('ready to submit')
  })
})

// --- EXTR-15-09 (task-835), QA pass: the document arm of the PARENT screen -------------
//
// CEN-2 and SW-3 prove a document literal is IN the source. Neither proves it RENDERS,
// that it renders in the RIGHT arm, or that `unit` reaches the component that reads it.
// Until these specs the only filename in this file was 'june.csv', so B1-B10 and both
// rejected surfaces had no document-arm oracle anywhere in the suite.
//
// Raw `textContent`, never getByText: getByText normalizes whitespace and so cannot see a
// doubled space left by splitting a paragraph into branches. SW-16 is that guard.

// One completed batch, no errors -- both frozen channels sit at zero, which is the only
// state that reaches B3's and B9's at-zero captions.
function cleanRun(ext: string): ImportBatch[] {
  return [batch({ filename: `june${ext}` })]
}

// The same batch with one error in each frozen channel: `rule_key` is what splits them
// (isAlreadyImported), so one error without it and one with it populates both.
function mixedRun(ext: string): ImportBatch[] {
  return [
    batch({
      filename: `june${ext}`,
      errors: [
        { row: 3, field: 'total', message: 'could not be read' },
        { row: 5, rule_key: 'duplicate_invoice', invoice_id: 'inv-9', message: 'already imported' },
      ],
    }),
  ]
}

// reviewShellStateAll is 'rejected' iff NO batch is 'completed'; one batch routes to
// RejectedFile, more than one to RejectedRun. Unequal rows_valid/rows_invalid so a
// swapped tile would be visible.
function rejectedFileRun(ext: string): ImportBatch[] {
  return [batch({ filename: `june${ext}`, status: 'failed', rows_valid: 40, rows_invalid: 7, errors: [] })]
}

// rows_valid 0 with no errors is what makes batchReason fall through to R7's sentence,
// which RejectedRun renders through filesStrip -- so this fixture covers B7 and R7 at once.
function rejectedRunRun(ext: string): ImportBatch[] {
  return [
    batch({ id: 'b1', filename: `june${ext}`, status: 'failed', rows_valid: 0, errors: [] }),
    batch({ id: 'b2', filename: `july${ext}`, status: 'failed', rows_valid: 0, errors: [] }),
  ]
}

// Dispatches the batch GET by id so a multi-batch run can be rendered; the shipped
// single-batch mockReviewFetch above is left alone.
function mockReviewFetchAll(batches: ImportBatch[], totals: typeof TOTALS) {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes('/imports/')) {
      const hit = batches.find((b) => url.includes(b.id))
      return hit == null
        ? Promise.reject(new Error(`unmocked batch url: ${url}`))
        : Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(hit) })
    }
    if (url.includes('/violation-summary')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve({ rules: [] }) })
    }
    if (url.includes('/invoices')) {
      const params = new URL(url).searchParams
      if (params.get('kept_as_is') === 'true') return Promise.resolve(listResponse(totals.keptTotal))
      if (params.get('needs_fix') === 'true') return Promise.resolve(listResponse(totals.failingTotal))
      if (params.get('status') === 'validated') return Promise.resolve(listResponse(totals.cleanTotal))
      if (params.get('status') === 'queued') return Promise.resolve(listResponse(totals.queuedTotal))
      if (params.get('limit') === '1') return Promise.resolve(listResponse(totals.allTotal))
      return Promise.resolve(listResponse(0))
    }
    return Promise.reject(new Error(`unmocked url: ${url}`))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const BATCH_ANCHOR = 'invoices imported'
const REJECTED_ANCHOR = 'Nothing was imported'

// Unmounts before returning so a caller can render the other arm into the same body.
async function renderArm(batches: ImportBatch[], anchor: string) {
  mockReviewFetchAll(batches, TOTALS)
  const { container } = render(<ReviewBatch ctx={reviewCtx(batches.map((b) => b.id))} />)
  await waitFor(() => expect(container.textContent ?? '').toContain(anchor))
  const text = container.textContent ?? ''
  const paragraphs = Array.from(container.querySelectorAll('p')).map((p) => p.textContent ?? '')
  cleanup()
  return { text, paragraphs }
}

// Both halves, always. One-sided containment passes on a screen rendering BOTH arms at
// once, and on a `unit` that never reached the branch.
function expectArm(text: string, mine: string[], theirs: string[], label: string) {
  for (const s of mine) expect(text, `${label} did not render ${JSON.stringify(s)}`).toContain(s)
  for (const s of theirs) expect(text, `${label} leaked the other unit's ${JSON.stringify(s)}`).not.toContain(s)
}

// Every spreadsheet string below is byte-identical to the pre-sweep source (AC-2).
const ARMS = {
  clean: {
    document: [
      '500 DOCUMENTS READ · SERVER VERDICT · RULE SET ', // R2, through reviewHeaderAll
      'Built from 480 documents. Every one of these exists in the ledger — fixing and submitting is what is left.', // B1
      '0 unreadable documents', // B2
      'Every document in this import could be read.', // B3
      '0 already in the register', // B8
      'Nothing in this import was already in the register.', // B9
    ],
    spreadsheet: [
      '500 ROWS READ · SERVER VERDICT · RULE SET ',
      'Built from 480 rows. Every one of these exists in the ledger — fixing and submitting is what is left.',
      '0 unreadable rows',
      'Every row in the file could be read.',
      '0 already imported',
      'Nothing in this file was already in your ledger.',
    ],
  },
  mixed: {
    document: [
      '1 unreadable documents', // B2 above zero
      '1 already in the register', // B8 above zero
      '1 invoices already in the register. Nothing to fix.', // B10
      'Unreadable documents (1)', // R3, the tab label, through reviewTabs
    ],
    spreadsheet: [
      '1 unreadable rows',
      '1 already imported',
      '1 invoices already in your ledger. Nothing to fix.',
      'Unreadable rows (1)',
    ],
  },
  rejectedFile: {
    document: [
      'The server rejected this file and created no invoices. This usually means nothing invoice-shaped could be found in it — a scan too poor to read, for example.', // B4
      'Documents stored', // B5
      'Documents quarantined', // B6
    ],
    spreadsheet: [
      'The server rejected this file and created no invoices. This usually means it held no data rows — a spreadsheet with only a header row, for example.',
      'Rows stored',
      'Rows quarantined',
    ],
  },
  rejectedRun: {
    document: [
      'The server rejected every file in this run and created no invoices. This usually means nothing invoice-shaped could be found in a file — a scan too poor to read, for example.', // B7
      '0 of 500 documents produced an invoice', // R7, through filesStrip
    ],
    spreadsheet: [
      'The server rejected every file in this run and created no invoices. This usually means a file held no data rows — a spreadsheet with only a header row, for example.',
      '0 of 500 rows produced an invoice',
    ],
  },
}

describe('EXTR-15-09 SW-12..SW-15 (AC-1/AC-3): the parent screen renders its unit branch', () => {
  it('SW-12: at zero errors a .pdf run renders R2, B1, B2, B3, B8 and B9 as documents; a .csv run is unchanged', async () => {
    const doc = await renderArm(cleanRun('.pdf'), BATCH_ANCHOR)
    expectArm(doc.text, ARMS.clean.document, ARMS.clean.spreadsheet, 'the document arm')

    const sheet = await renderArm(cleanRun('.csv'), BATCH_ANCHOR)
    expectArm(sheet.text, ARMS.clean.spreadsheet, ARMS.clean.document, 'the spreadsheet arm')
  })

  it('SW-13: above zero a .pdf run renders B2, B8, B10 and the unreadable tab label as documents', async () => {
    const doc = await renderArm(mixedRun('.pdf'), BATCH_ANCHOR)
    expectArm(doc.text, ARMS.mixed.document, ARMS.mixed.spreadsheet, 'the document arm')

    const sheet = await renderArm(mixedRun('.csv'), BATCH_ANCHOR)
    expectArm(sheet.text, ARMS.mixed.spreadsheet, ARMS.mixed.document, 'the spreadsheet arm')
  })

  it('SW-14: the single-file rejected surface renders B4, B5 and B6 in the unit it was given', async () => {
    const doc = await renderArm(rejectedFileRun('.pdf'), REJECTED_ANCHOR)
    expectArm(doc.text, ARMS.rejectedFile.document, ARMS.rejectedFile.spreadsheet, 'the document arm')

    const sheet = await renderArm(rejectedFileRun('.csv'), REJECTED_ANCHOR)
    expectArm(sheet.text, ARMS.rejectedFile.spreadsheet, ARMS.rejectedFile.document, 'the spreadsheet arm')
  })

  it('SW-15: the multi-file rejected surface renders B7 and R7 in the unit it was given', async () => {
    const doc = await renderArm(rejectedRunRun('.pdf'), REJECTED_ANCHOR)
    expectArm(doc.text, ARMS.rejectedRun.document, ARMS.rejectedRun.spreadsheet, 'the document arm')

    const sheet = await renderArm(rejectedRunRun('.csv'), REJECTED_ANCHOR)
    expectArm(sheet.text, ARMS.rejectedRun.spreadsheet, ARMS.rejectedRun.document, 'the spreadsheet arm')
  })
})

describe('EXTR-15-09 SW-18 (AC-6): B5 and B6 are two whole Tiles per arm, and only one arm renders', () => {
  // The census needles `caption="Rows stored"` as written, so the branch had to duplicate
  // the whole <Tile> rather than branch its `caption` expression. A duplication is one
  // dropped ternary away from rendering BOTH arms and widening the row from three tiles to
  // five -- which is a layout change, and AC-6 forbids one.
  it('SW-18: the rejected surface renders exactly three tiles in both units', async () => {
    for (const ext of ['.pdf', '.csv']) {
      mockReviewFetchAll(rejectedFileRun(ext), TOTALS)
      const { container } = render(<ReviewBatch ctx={reviewCtx(['b1'])} />)
      await waitFor(() => expect(container.textContent ?? '').toContain(REJECTED_ANCHOR))

      // The unbranched tile is the anchor: its caption node's grandparent is the tile row.
      const anchor = Array.from(container.querySelectorAll('div')).find((d) => d.textContent === 'Invoices created')
      expect(anchor, `${ext}: no "Invoices created" tile`).toBeTruthy()
      const row = anchor!.parentElement!.parentElement!

      expect(row.children.length, `${ext}: the tile row is not three tiles wide`).toBe(3)
      cleanup()
    }
  })
})

// Both tab BODIES are unmounted until their tab is clicked, so nothing in the suite
// rendered ReviewBatch's `unit={unit}` INTO either one -- the tab tests construct the tabs
// directly and supply their own `unit`, and SW-10 only scans the source for the attribute.
// A hard-coded `unit="spreadsheet"` at either call site is invisible without this.
const TAB_ARMS = {
  unreadable: {
    label: { document: 'Unreadable documents (1)', spreadsheet: 'Unreadable rows (1)' },
    document: ['1 documents never became invoices', 'The extractor could not read them'], // U2, U3
    spreadsheet: ['1 rows never became invoices', 'The importer could not read them'],
  },
  alreadyImported: {
    label: { document: 'Already imported (1)', spreadsheet: 'Already imported (1)' },
    document: ['1 documents were already in the register', 'Invoice already in the register'], // A3, A6
    spreadsheet: ['1 rows were already in your ledger', 'Invoice already in your ledger'],
  },
}

describe('EXTR-15-09 SW-17 (AC-4/AC-9): the unit reaches both tab bodies, not just the tab strip', () => {
  it('SW-17: opening either tab in a .pdf run shows document copy; the same clicks in a .csv run are unchanged', async () => {
    for (const unit of ['document', 'spreadsheet'] as const) {
      const ext = unit === 'document' ? '.pdf' : '.csv'
      mockReviewFetchAll(mixedRun(ext), TOTALS)
      const { container } = render(<ReviewBatch ctx={reviewCtx(['b1'])} />)
      await waitFor(() => expect(container.textContent ?? '').toContain(BATCH_ANCHOR))

      for (const tab of [TAB_ARMS.unreadable, TAB_ARMS.alreadyImported]) {
        const button = Array.from(container.querySelectorAll('button.pf-tab')).find((b) => b.textContent === tab.label[unit])
        expect(button, `no tab button labelled ${tab.label[unit]}`).toBeTruthy()
        fireEvent.click(button as HTMLElement)

        const other = unit === 'document' ? 'spreadsheet' : 'document'
        expectArm(container.textContent ?? '', tab[unit], tab[other], `the ${unit} arm of ${tab.label[unit]}`)
      }
      cleanup()
    }
  })
})

// Whole-element equality against the pre-sweep text (ReviewBatch.tsx is byte-identical
// between the AC-2 freeze commit aaee0c3d and the pre-implementation HEAD 70f07853). B1,
// B4 and B7 were single text nodes and are now a text node plus a branch, which is exactly
// where a doubled or dropped space gets in; containment cannot see one and getByText
// normalizes it away. The three are asserted as the WHOLE paragraph, so a stray space at
// either edge fails too.
const FROZEN_SPREADSHEET_PARAGRAPHS = [
  'Built from 480 rows. Every one of these exists in the ledger — fixing and submitting is what is left.',
  'The server rejected this file and created no invoices. This usually means it held no data rows — a spreadsheet with only a header row, for example.',
  'The server rejected every file in this run and created no invoices. This usually means a file held no data rows — a spreadsheet with only a header row, for example.',
]

describe('EXTR-15-09 SW-16 (AC-2): the restructured paragraphs render byte-identically', () => {
  it('SW-16: each restructured spreadsheet paragraph is whole-element equal to its pre-sweep text', async () => {
    const surfaces = [
      await renderArm(cleanRun('.csv'), BATCH_ANCHOR),
      await renderArm(rejectedFileRun('.csv'), REJECTED_ANCHOR),
      await renderArm(rejectedRunRun('.csv'), REJECTED_ANCHOR),
    ]
    const rendered = surfaces.flatMap((s) => s.paragraphs)

    // Control for the equality claims: the three surfaces really rendered prose, so a
    // component returning null could not satisfy them by contributing nothing.
    expect(rendered.length, 'the three surfaces rendered no paragraphs at all').toBeGreaterThanOrEqual(3)
    for (const frozen of FROZEN_SPREADSHEET_PARAGRAPHS) expect(rendered).toContain(frozen)
  })

  it('SW-16 whitespace: no paragraph in either unit carries a doubled space', async () => {
    const paragraphs: string[] = []
    for (const ext of ['.csv', '.pdf']) {
      paragraphs.push(...(await renderArm(cleanRun(ext), BATCH_ANCHOR)).paragraphs)
      paragraphs.push(...(await renderArm(rejectedFileRun(ext), REJECTED_ANCHOR)).paragraphs)
      paragraphs.push(...(await renderArm(rejectedRunRun(ext), REJECTED_ANCHOR)).paragraphs)
    }

    // Floor, so the absence claim below cannot pass over an empty list.
    expect(paragraphs.length, 'no paragraphs were collected').toBeGreaterThanOrEqual(6)
    expect(paragraphs.filter((p) => /\s{2}/.test(p))).toEqual([])
  })
})
