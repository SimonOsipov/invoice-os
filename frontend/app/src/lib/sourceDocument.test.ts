// RED specs (DOC-02-04, Mode A) — pin lib/sourceDocument.ts's contract before the
// executor implements the bodies. One describe per exported symbol. vitest environment
// is 'node' (vitest.config.ts:5) — no jsdom, no DOM.
//
// Every spec below currently fails because its target export's stub body throws
// new Error('not implemented') before ever returning/fetching anything — that IS the
// correct RED reason (assertion / thrown-error), never an import/compile error.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { ApiError } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import {
  classifyDocument,
  contiguousRanges,
  describeSourceRows,
  fetchDocumentBytes,
  formatBytes,
  getDocumentSheet,
  getSourceDocument,
  numberSheetRows,
  rangeLabel,
  rowsWithinSheet,
  sheetWindow,
  sourceDocumentState,
} from './sourceDocument'
import type { SourceDocumentMeta, SourceDocumentRecord, SourceDocumentResponse, DocumentSheet, LoadStatus, SourceDocumentState } from './sourceDocument'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const BASE = 'https://gw'
const INVOICE_ID = 'a1b2c3d4-e5f6-4a1b-9c2d-3e4f5a6b7c8d'
const DOCUMENT_ID = 'b2c3d4e5-f6a7-4b2c-8d3e-4f5a6b7c8d9e'
const CONTENT_HASH = '0123456789abcdef'.repeat(4)
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

function mkDocument(overrides: Partial<SourceDocumentRecord> = {}): SourceDocumentRecord {
  return {
    id: DOCUMENT_ID,
    filename: null,
    declared_content_type: null,
    size_bytes: 1024,
    content_hash: CONTENT_HASH,
    uploaded_at: '2026-08-01T00:00:00Z',
    uploaded_by: null,
    invoices_created: 1,
    other_invoice_rows: [],
    ...overrides,
  }
}

const SPREADSHEET_DOC = mkDocument({ filename: 'a.csv' })
const PDF_DOC = mkDocument({ filename: 'a.pdf' })
const IMAGE_DOC = mkDocument({ filename: 'a.png' })
const UNRENDERABLE_DOC = mkDocument({ filename: 'a.zip', declared_content_type: 'application/zip' })

function mkMeta(status: LoadStatus, document: SourceDocumentRecord | null): SourceDocumentMeta {
  if (status !== 'ready') return { status, value: null }
  return { status, value: { invoice_id: INVOICE_ID, source_rows: null, document } }
}

// -- classifyDocument (AC1) --------------------------------------------------------

describe('classifyDocument', () => {
  it('recognized extension decides', () => {
    expect(classifyDocument('a.csv', null)).toBe('spreadsheet')
    expect(classifyDocument('A.XLSX', null)).toBe('spreadsheet')
    expect(classifyDocument('a.pdf', null)).toBe('pdf')
    expect(classifyDocument('a.PNG', null)).toBe('image')
    expect(classifyDocument('a.jpg', null)).toBe('image')
    expect(classifyDocument('a.jpeg', null)).toBe('image')
    expect(classifyDocument('a.webp', null)).toBe('image')
  })

  it('a recognized extension beats a conflicting content type', () => {
    expect(classifyDocument('x.csv', 'application/pdf')).toBe('spreadsheet')
    expect(classifyDocument('x.pdf', 'text/csv')).toBe('pdf')
  })

  it('an unrecognized extension falls through, it is not a verdict', () => {
    expect(classifyDocument('x.zip', 'text/csv')).toBe('spreadsheet')
    expect(classifyDocument('x.txt', 'text/plain')).toBe('spreadsheet')
    expect(classifyDocument('x.zip', 'application/zip')).toBe('unrenderable')
  })

  it('null filename falls back to the content type', () => {
    expect(classifyDocument(null, 'application/pdf')).toBe('pdf')
    expect(classifyDocument(null, 'image/webp')).toBe('image')
    expect(classifyDocument(null, null)).toBe('unrenderable')
    expect(classifyDocument('', '')).toBe('unrenderable')
  })

  it('content-type parameters are stripped', () => {
    expect(classifyDocument(null, 'text/csv; charset=utf-8')).toBe('spreadsheet')
  })

  // QA adversarial (DOC-02-04).
  it('an uppercase .CSV extension resolves the same as lowercase', () => {
    expect(classifyDocument('A.CSV', null)).toBe('spreadsheet')
    expect(classifyDocument('invoice.CsV', null)).toBe('spreadsheet')
  })

  it('a double extension keys off the LAST segment only', () => {
    // "report.csv.zip" -> extension "zip", unrecognized -> falls through to content type,
    // exactly like any other unrecognized extension (AC1).
    expect(classifyDocument('report.csv.zip', 'text/csv')).toBe('spreadsheet')
    expect(classifyDocument('report.csv.zip', null)).toBe('unrenderable')
  })

  it('multiple content-type parameters are all stripped, not just the first', () => {
    expect(classifyDocument(null, 'text/csv; charset=utf-8; boundary=xyz')).toBe('spreadsheet')
  })
})

// -- sourceDocumentState (AC2) ------------------------------------------------------

describe('sourceDocumentState', () => {
  it('the full resolution table', () => {
    const cases: Array<[SourceDocumentMeta, LoadStatus, LoadStatus, SourceDocumentState]> = [
      [mkMeta('loading', null), 'idle', 'idle', 'loading'],
      [mkMeta('ready', null), 'idle', 'idle', 'no-source'],
      [mkMeta('ready', SPREADSHEET_DOC), 'ready', 'idle', 'spreadsheet'],
      [mkMeta('ready', PDF_DOC), 'idle', 'ready', 'pdf'],
      [mkMeta('ready', IMAGE_DOC), 'idle', 'ready', 'image'],
      [mkMeta('ready', UNRENDERABLE_DOC), 'idle', 'idle', 'unrenderable'],
      [mkMeta('error', null), 'idle', 'idle', 'failed'],
    ]
    const results = cases.map(([meta, sheet, bytes]) => sourceDocumentState(meta, sheet, bytes))
    cases.forEach(([, , , expected], i) => expect(results[i]).toBe(expected))
    // A resolver that can never emit one of the 7 states (e.g. 'image') would still pass
    // the per-case checks above if it happened to collide with another state's value.
    expect(new Set(results).size).toBe(7)
  })

  it('an unresolved or failed meta never reads as no-source', () => {
    expect(sourceDocumentState(mkMeta('loading', null), 'idle', 'idle')).toBe('loading')
    expect(sourceDocumentState(mkMeta('error', null), 'idle', 'idle')).toBe('failed')
    expect(sourceDocumentState(mkMeta('idle', null), 'idle', 'idle')).toBe('loading')
  })

  it('no document wins over every fetch status', () => {
    const meta = mkMeta('ready', null)
    const statuses: LoadStatus[] = ['idle', 'loading', 'ready', 'error']
    for (const sheet of statuses) {
      for (const bytes of statuses) {
        expect(sourceDocumentState(meta, sheet, bytes)).toBe('no-source')
      }
    }
  })

  it('the channel follows the kind', () => {
    expect(sourceDocumentState(mkMeta('ready', SPREADSHEET_DOC), 'error', 'ready')).toBe('failed')
    expect(sourceDocumentState(mkMeta('ready', SPREADSHEET_DOC), 'ready', 'error')).toBe('spreadsheet')
    expect(sourceDocumentState(mkMeta('ready', PDF_DOC), 'error', 'ready')).toBe('pdf')
  })

  // QA adversarial (DOC-02-04): the resolution table above only exercises documents
  // classified by extension. This proves the state resolver generalizes to the
  // content-type fallback path too, not just the fixtures with a recognized extension.
  it('state resolution also generalizes over content-type-classified documents', () => {
    const contentTypeOnlyPdf = mkDocument({ filename: null, declared_content_type: 'application/pdf' })
    const contentTypeOnlyImage = mkDocument({ filename: null, declared_content_type: 'image/webp' })
    expect(sourceDocumentState(mkMeta('ready', contentTypeOnlyPdf), 'idle', 'ready')).toBe('pdf')
    expect(sourceDocumentState(mkMeta('ready', contentTypeOnlyImage), 'idle', 'ready')).toBe('image')
  })
})

// -- sheetWindow (AC3) ---------------------------------------------------------------

describe('sheetWindow', () => {
  it("the design's formula, as literals", () => {
    expect(sheetWindow(3000, 600, 1479)).toEqual({ start: 92, end: 142 })
  })

  it('clamps a mis-measured viewport at both bounds', () => {
    expect(sheetWindow(0, 44000, 100000)).toEqual({ start: 0, end: 62 }) // h clamped to 1200
    expect(sheetWindow(0, 10, 100000)).toEqual({ start: 0, end: 30 }) // h clamped to 240
    expect(sheetWindow(0, 1200, 100000).end).toBe(62) // upper bound itself
    expect(sheetWindow(0, 240, 100000).end).toBe(30) // lower bound itself
    expect(sheetWindow(0, 600, 100000).end).toBe(42) // in range, untouched
  })

  it('a non-finite measured viewport cannot degenerate the window', () => {
    const nanResult = sheetWindow(0, NaN, 100000)
    const infResult = sheetWindow(0, Infinity, 100000)
    expect(nanResult).toEqual({ start: 0, end: 30 })
    expect(infResult).toEqual({ start: 0, end: 30 })
    expect(Number.isFinite(nanResult.start)).toBe(true)
    expect(Number.isFinite(nanResult.end)).toBe(true)
    expect(Number.isFinite(infResult.start)).toBe(true)
    expect(Number.isFinite(infResult.end)).toBe(true)
  })

  it('never exceeds the row total', () => {
    const overscrolled = sheetWindow(999999, 600, 1479)
    expect(overscrolled.end).toBe(1479)
    expect(overscrolled.start).toBeLessThanOrEqual(overscrolled.end)
    expect(overscrolled.start).toBeGreaterThanOrEqual(0)

    expect(sheetWindow(0, 600, 0)).toEqual({ start: 0, end: 0 })

    const negativeScroll = sheetWindow(-90, 600, 1479)
    expect(negativeScroll.start).toBe(0)
  })

  // QA adversarial (DOC-02-04). AC3 clamps measuredViewportH only -- scrollTop is a real
  // DOM scrollTop, never NaN in practice, so this pins today's behavior rather than fixing it.
  it('a non-finite scrollTop is not guarded and degenerates the window -- pinned, not fixed', () => {
    const result = sheetWindow(NaN, 600, 100000)
    expect(Number.isNaN(result.start)).toBe(true)
    expect(Number.isNaN(result.end)).toBe(true)
  })

  it('scrollTop far beyond the content clamps both bounds to total', () => {
    expect(sheetWindow(999_999_999, 600, 100)).toEqual({ start: 100, end: 100 })
  })

  // Surprising but pinned: -90 (the architect's own iOS rubber-band literal) still shows
  // rows ({end: 39}), but a scrollTop past roughly -1260 collapses the window to fully
  // empty ({0,0}) even though total > 0 -- worse than scrollTop=0's populated window. Real
  // browsers bound rubber-band overscroll well inside this range, so it is not reachable
  // in practice, but the asymmetry is real and undocumented.
  it('an extreme negative scrollTop collapses the window to empty, unlike scrollTop=0', () => {
    expect(sheetWindow(-90, 600, 1479)).toEqual({ start: 0, end: 39 })
    expect(sheetWindow(-999_999, 600, 1479)).toEqual({ start: 0, end: 0 })
    expect(sheetWindow(0, 600, 1479)).toEqual({ start: 0, end: 42 })
  })

  it('a zero row total yields an empty window regardless of scroll position', () => {
    expect(sheetWindow(500, 600, 0)).toEqual({ start: 0, end: 0 })
    expect(sheetWindow(-90, 600, 0)).toEqual({ start: 0, end: 0 })
  })

  it('a row total smaller than the overscan clamps end to total, not to the overscan term', () => {
    expect(sheetWindow(0, 600, 5)).toEqual({ start: 0, end: 5 })
  })
})

// -- numberSheetRows (AC7) ------------------------------------------------------------

describe('numberSheetRows', () => {
  it('binds the sheet number, i+2', () => {
    expect(numberSheetRows([['a'], ['b'], ['c']])).toEqual([
      { sheetRow: 2, cells: ['a'] },
      { sheetRow: 3, cells: ['b'] },
      { sheetRow: 4, cells: ['c'] },
    ])
    expect(numberSheetRows([])).toEqual([])
  })

  it('filtering cannot renumber', () => {
    const rows = [['r0'], ['r1'], ['r2'], ['r3'], ['r4'], ['r5']]
    const numbered = numberSheetRows(rows)
    const filtered = [numbered[1], numbered[4]]
    expect(filtered.map((r) => r.sheetRow)).toEqual([3, 6])
  })
})

// -- contiguousRanges (AC4) -----------------------------------------------------------

describe('contiguousRanges', () => {
  it('runs, singletons and gaps', () => {
    expect(contiguousRanges([44, 45, 46, 47])).toEqual([[44, 47]])
    expect(contiguousRanges([44])).toEqual([[44, 44]])
    expect(contiguousRanges([44, 46])).toEqual([
      [44, 44],
      [46, 46],
    ])
    expect(contiguousRanges([44, 45, 46, 51])).toEqual([
      [44, 46],
      [51, 51],
    ])
    expect(contiguousRanges([])).toEqual([])
    expect(contiguousRanges(null)).toEqual([])
  })

  it('unsorted and duplicated input, which the CHECK permits', () => {
    expect(contiguousRanges([47, 44, 46, 45])).toEqual([[44, 47]])
    expect(contiguousRanges([7, 3])).toEqual([
      [3, 3],
      [7, 7],
    ])
    expect(contiguousRanges([44, 44, 45])).toEqual([[44, 45]])
  })
})

// -- describeSourceRows (AC4) ---------------------------------------------------------

describe('describeSourceRows', () => {
  const NOT_RECORDED = 'The rows of this file that became this invoice were not recorded.'

  it('the exact sentence per row shape', () => {
    expect(describeSourceRows([44], 'spreadsheet')).toBe('Row 44 of this file became this invoice.')
    expect(describeSourceRows([44, 45, 46, 47], 'spreadsheet')).toBe('Rows 44–47 of this file became this invoice.')
    expect(describeSourceRows([44, 46], 'spreadsheet')).toBe('Rows 44 and 46 of this file became this invoice.')
    expect(describeSourceRows([44, 45, 46, 51], 'spreadsheet')).toBe('Rows 44–46 and 51 of this file became this invoice.')
    expect(describeSourceRows(null, 'spreadsheet')).toBe(NOT_RECORDED)
    expect(describeSourceRows([], 'spreadsheet')).toBe(NOT_RECORDED)
  })

  it('the not-recorded sentence contains no digits', () => {
    const sentence = describeSourceRows(null, 'spreadsheet')
    expect(sentence).not.toMatch(/\d/)
    expect(sentence.length).toBeGreaterThan(0)
  })

  it('kind decides before rows', () => {
    expect(describeSourceRows(null, 'image')).toBe('This photograph became this invoice.')
    expect(describeSourceRows(null, 'unrenderable')).toBe('Stored, but the previewer cannot render this format.')
    expect(describeSourceRows(null, 'pdf')).toBe('The page of this document that became this invoice was not recorded.')
    expect(describeSourceRows([44], 'image')).toBe('This photograph became this invoice.')
  })
})

// -- rangeLabel (AC4) ------------------------------------------------------------------

describe('rangeLabel', () => {
  it('the toolbar short form', () => {
    expect(rangeLabel([44])).toBe('ROW 44')
    expect(rangeLabel([44, 45, 46, 47])).toBe('ROWS 44–47')
    expect(rangeLabel([44, 46])).toBe('ROWS 44 AND 46')
    expect(rangeLabel([44, 45, 46, 51])).toBe('ROWS 44–46 AND 51')
    expect(rangeLabel(null)).toBeNull()
    expect(rangeLabel([])).toBeNull()
  })
})

// -- QA adversarial: contiguousRanges / describeSourceRows / rangeLabel edges (DOC-02-04) --

describe('contiguousRanges / describeSourceRows / rangeLabel: adversarial edges', () => {
  it('two adjacent rows form one range, not two singletons', () => {
    expect(contiguousRanges([44, 45])).toEqual([[44, 45]])
    expect(describeSourceRows([44, 45], 'spreadsheet')).toBe('Rows 44–45 of this file became this invoice.')
    expect(rangeLabel([44, 45])).toBe('ROWS 44–45')
  })

  it('two rows far apart stay two singletons, joined by "and"', () => {
    expect(contiguousRanges([10, 9999])).toEqual([
      [10, 10],
      [9999, 9999],
    ])
    expect(describeSourceRows([10, 9999], 'spreadsheet')).toBe('Rows 10 and 9999 of this file became this invoice.')
    expect(rangeLabel([10, 9999])).toBe('ROWS 10 AND 9999')
  })

  it('an all-duplicate row list collapses to one singleton range', () => {
    expect(contiguousRanges([7, 7, 7, 7])).toEqual([[7, 7]])
    expect(describeSourceRows([7, 7, 7, 7], 'spreadsheet')).toBe('Row 7 of this file became this invoice.')
    expect(rangeLabel([7, 7, 7, 7])).toBe('ROW 7')
  })

  // Design says "pluralise honestly", not "keep it short" -- there is no cap on range
  // count. 50 non-contiguous rows already produce a 237-char sentence (measured below);
  // scaling to a genuinely messy invoice (hundreds of scattered rows) would read as a
  // wall of numbers, not a caption. Advisory: no design-system rule caps this, so this is
  // NOT filed as a defect -- flagging for the human, not bouncing to the executor.
  it('a long non-contiguous set stays structurally correct, but the sentence is not capped', () => {
    const rows: number[] = []
    for (let n = 2; n <= 100; n += 2) rows.push(n)

    const ranges = contiguousRanges(rows)
    expect(ranges).toHaveLength(50)
    expect(ranges.every(([from, to]) => from === to)).toBe(true) // every entry a singleton

    const sentence = describeSourceRows(rows, 'spreadsheet')
    expect(sentence.startsWith('Rows 2, 4, 6,')).toBe(true)
    expect(sentence.endsWith('and 100 of this file became this invoice.')).toBe(true)
    expect(sentence).toContain(', 98 and 100') // final conjunction uses "and", not a comma
    expect(sentence.length).toBe(237) // pinned so a future change to the join logic is visible here

    const label = rangeLabel(rows)
    expect(label?.startsWith('ROWS 2, 4, 6,')).toBe(true)
    expect(label?.endsWith('AND 100')).toBe(true)
  })
})

// -- formatBytes (AC4) -------------------------------------------------------------------

describe('formatBytes', () => {
  it('binary-base formatting, and the negative/NaN guards', () => {
    expect(formatBytes(624640)).toBe('610 KB')
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
    expect(formatBytes(1024)).toBe('1 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(15728640)).toBe('15 MB')
    expect(formatBytes(1048575)).toBe('1 MB')
    expect(formatBytes(-1)).toBe('0 B')
    expect(formatBytes(NaN)).toBe('0 B')
  })
})

// -- rowsWithinSheet (AC8, QA finding F3) -------------------------------------------------

describe('rowsWithinSheet', () => {
  it("a truncated window splits the invoice's rows", () => {
    expect(rowsWithinSheet([4999, 5001, 5002], 5000)).toEqual({ present: [4999, 5001], missing: [5002] })
    expect(rowsWithinSheet([44, 47], 5000)).toEqual({ present: [44, 47], missing: [] })
    expect(rowsWithinSheet(null, 5000)).toEqual({ present: [], missing: [] })
  })

  // sheetRow(i)=i+2 and the endpoint returns data rows 0..rowsReturned-1, so the last row
  // actually sent is rowsReturned+1.
  it('the boundary is rows_returned + 1', () => {
    expect(rowsWithinSheet([5001], 5000)).toEqual({ present: [5001], missing: [] })
    expect(rowsWithinSheet([5002], 5000)).toEqual({ present: [], missing: [5002] })
    expect(rowsWithinSheet([5000], 5000)).toEqual({ present: [5000], missing: [] })
    expect(rowsWithinSheet([2], 5000)).toEqual({ present: [2], missing: [] })
    expect(rowsWithinSheet([1], 5000)).toEqual({ present: [], missing: [1] }) // row 1 is the header
  })
})

// -- fetchers (AC5) -----------------------------------------------------------------------

interface MockJsonResponse {
  ok: boolean
  status: number
  statusText?: string
  json: () => Promise<unknown>
}

function mockFetchOnce(response: MockJsonResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Tolerates both a synchronous throw (today's stub) and an eventual async rejection.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

describe('getSourceDocument', () => {
  it('url, bearer and the resolved body', async () => {
    const body: SourceDocumentResponse = { invoice_id: INVOICE_ID, source_rows: null, document: mkDocument() }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(body) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getSourceDocument(af, BASE, INVOICE_ID)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`${BASE}/api/invoice/v1/invoices/${INVOICE_ID}/source-document`)
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer tok')
    expect(result.invoice_id).toMatch(UUID_RE)
    expect(result.document?.content_hash).toMatch(/^[0-9a-f]{64}$/)
  })

  it('a 401 surfaces as ApiError through the real seam', async () => {
    mockFetchOnce({ ok: false, status: 401, statusText: 'Unauthorized', json: () => Promise.resolve({ error: 'token expired' }) })
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    const err = await captureRejection(() => getSourceDocument(af, BASE, INVOICE_ID))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(401)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })
})

describe('getDocumentSheet', () => {
  it('url, bearer and the resolved body', async () => {
    const sheet: DocumentSheet = {
      format: 'csv',
      delimiter: ',',
      encoding: 'utf-8',
      columns: ['Invoice No', 'Total'],
      rows: [['INV-1', '100.00']],
      rows_total: 1,
      rows_returned: 1,
      truncated: false,
    }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(sheet) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getDocumentSheet(af, BASE, DOCUMENT_ID)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`${BASE}/api/invoice/v1/documents/${DOCUMENT_ID}/sheet`)
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer tok')
    expect(result.columns.length).toBeGreaterThan(0)
    expect(result.rows.length).toBeGreaterThan(0)
    expect(result.rows_total).toBeGreaterThanOrEqual(result.rows_returned)
  })

  it('a 404 surfaces as ApiError', async () => {
    mockFetchOnce({ ok: false, status: 404, statusText: 'Not Found', json: () => Promise.resolve({ error: 'not found' }) })
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    const err = await captureRejection(() => getDocumentSheet(af, BASE, DOCUMENT_ID))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(404)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })
})

describe('getSourceDocument & getDocumentSheet: id encoding', () => {
  it('both fetchers encode the id path segment', async () => {
    const idWithSlash = 'a/b'
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const fetchMock1 = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ invoice_id: INVOICE_ID, source_rows: null, document: null }) })
    await getSourceDocument(af, BASE, idWithSlash)
    const [url1] = fetchMock1.mock.calls[0] as [string]
    expect(url1).toBe(`${BASE}/api/invoice/v1/invoices/a%2Fb/source-document`)

    const emptySheet: DocumentSheet = { format: 'csv', delimiter: ',', encoding: 'utf-8', columns: [], rows: [], rows_total: 0, rows_returned: 0, truncated: false }
    const fetchMock2 = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(emptySheet) })
    await getDocumentSheet(af, BASE, idWithSlash)
    const [url2] = fetchMock2.mock.calls[0] as [string]
    expect(url2).toBe(`${BASE}/api/invoice/v1/documents/a%2Fb/sheet`)
  })
})

// -- fetchDocumentBytes (AC6) ---------------------------------------------------------

function mockBytesFetch(bytes: number[], opts: { ok?: boolean; status?: number; statusText?: string } = {}) {
  const buffer = new Uint8Array(bytes).buffer
  const fetchMock = vi.fn().mockResolvedValue({
    ok: opts.ok ?? true,
    status: opts.status ?? 200,
    statusText: opts.statusText ?? 'OK',
    arrayBuffer: () => Promise.resolve(buffer),
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('fetchDocumentBytes', () => {
  it('re-types the octet-stream bytes', async () => {
    const fetchMock = mockBytesFetch([1, 2, 3, 4])
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-1')

    const result = await fetchDocumentBytes(() => 'tok', BASE, DOCUMENT_ID, 'pdf', 'a.pdf')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const blob = createObjectURL.mock.calls[0][0] as Blob
    expect(blob.type).toBe('application/pdf')
    expect(blob.size).toBe(4) // a size floor -- an empty blob would still pass a type-only check
    expect(result.url).toBe('blob:test-1')
  })

  it('image mime follows the filename', async () => {
    const cases: Array<[string, string]> = [
      ['a.png', 'image/png'],
      ['a.jpeg', 'image/jpeg'],
      ['a.webp', 'image/webp'],
    ]
    for (const [filename, expectedType] of cases) {
      mockBytesFetch([1, 2, 3, 4])
      const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-1')
      await fetchDocumentBytes(() => 'tok', BASE, DOCUMENT_ID, 'image', filename)
      const blob = createObjectURL.mock.calls[0][0] as Blob
      expect(blob.type).toBe(expectedType)
      createObjectURL.mockRestore()
    }
  })

  it('release revokes exactly the url it created', async () => {
    mockBytesFetch([1, 2, 3, 4])
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-1')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})

    const { release } = await fetchDocumentBytes(() => 'tok', BASE, DOCUMENT_ID, 'pdf', 'a.pdf')

    release()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:test-1')

    release() // idempotent -- no second revoke
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
  })

  it('no download affordance is constructed', async () => {
    const fetchMock = mockBytesFetch([1, 2, 3, 4])
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-1')

    await fetchDocumentBytes(() => 'tok', BASE, DOCUMENT_ID, 'pdf', 'a.pdf')

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(Array.from(new Headers(init.headers).keys())).toEqual(['authorization'])

    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'sourceDocument.ts'), 'utf8')
    expect(src).not.toContain('a.click(')
    expect(src).not.toContain('download=')
    expect(src).not.toContain('downloadGlyph')
  })

  // QA adversarial (DOC-02-04): the shipped spec only calls release() twice. Confirm the
  // idempotency guard holds under repeated calls, not just a second one.
  it('release stays idempotent across many repeated calls', async () => {
    mockBytesFetch([1, 2, 3, 4])
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test-1')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})

    const { release } = await fetchDocumentBytes(() => 'tok', BASE, DOCUMENT_ID, 'pdf', 'a.pdf')
    release()
    release()
    release()
    release()

    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:test-1')
  })
})
