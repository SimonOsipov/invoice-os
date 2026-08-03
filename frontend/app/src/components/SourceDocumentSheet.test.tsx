// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Every hard number below is derived from jsdom's `clientHeight === 0`: sheetWindow clamps
// the viewport to 240, i.e. 8 rows, so the window is 30 long while start is pinned at 0 and
// 38 once it is not. Do NOT mock clientHeight — the clamp is the thing under test, and a
// mocked height would make the window depend on a number no browser reports.

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { DocumentSheet, SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { SourceDocumentModal } from './SourceDocumentModal'
import { SourceDocumentSheet } from './SourceDocumentSheet'
import type { SourceDocumentAsync } from './SourceDocumentStates'

const COLUMNS = ['Invoice No', 'Issue Date', 'Buyer TIN', 'Customer', 'Curr', 'Net', 'VAT', 'Total', 'Item', 'Qty', 'Unit Price']

const INVOICE_ROWS = [44, 45, 46, 47]

// Column 0 carries the row's own sheet number, so every row is identifiable by CONTENT.
// Identity by DOM index is what made the lib-level numbering spec vacuous.
function makeRows(n: number): string[][] {
  return Array.from({ length: n }, (_, i) => [`INV-${i + 2}`, ...COLUMNS.slice(1).map((c) => `${c} ${i + 2}`)])
}

function sheet(rowCount: number, over: Partial<DocumentSheet> = {}): DocumentSheet {
  return {
    format: 'xlsx',
    delimiter: null,
    encoding: null,
    columns: COLUMNS,
    rows: makeRows(rowCount),
    rows_total: rowCount,
    rows_returned: rowCount,
    truncated: false,
    ...over,
  }
}

function renderSheet(s: DocumentSheet, sourceRows: number[] | null, otherInvoiceRows: number[] = []) {
  return render(<SourceDocumentSheet sheet={s} sourceRows={sourceRows} otherInvoiceRows={otherInvoiceRows} />)
}

function allRows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-sheet-row]'))
}

function sheetNumbers(els: HTMLElement[]): number[] {
  return els.map((el) => Number(el.getAttribute('data-sheet-row')))
}

/** The row holding a given first-cell value. Identity by content, never by position. */
function rowByCell(text: string): HTMLElement {
  const hits = allRows().filter((el) => el.children[1]?.textContent === text)
  expect(hits).toHaveLength(1) // floor: the fixture really is unambiguous
  return hits[0]
}

function labelOf(row: HTMLElement): string {
  const cell = row.querySelector<HTMLElement>('[data-testid="sheet-row-number"]')
  expect(cell).not.toBeNull()
  const text = cell?.textContent ?? ''
  expect(text).toMatch(/^\d+$/) // floor: a well-formed number, not "" and not prose
  return text
}

function scrollBox(): HTMLElement {
  return screen.getByTestId('sheet-scroll')
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('SourceDocumentSheet', () => {
  it("marks exactly the invoice's rows", () => {
    renderSheet(sheet(1479), INVOICE_ROWS)

    const marked = screen.getAllByTestId('sheet-row-marked')
    expect(marked).toHaveLength(4)
    expect(sheetNumbers(marked).map(String)).toEqual(['44', '45', '46', '47'])
    expect(screen.getAllByTestId('sheet-row').length).toBeGreaterThan(0)

    for (const row of marked) {
      expect(row.style.background).toBe('var(--accent-10)')
      expect(row.style.boxShadow).toBe('inset 2px 0 0 var(--accent)')
    }
  })

  it('row labels are sheet numbers in both scope modes', async () => {
    renderSheet(sheet(1479), INVOICE_ROWS)

    expect(labelOf(rowByCell('INV-44'))).toBe('44')

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))

    // The SAME row, re-found by its cell content, must still carry the file's number.
    expect(labelOf(rowByCell('INV-44'))).toBe('44')

    const labels = screen.getAllByTestId('sheet-row-number').map((el) => el.textContent)
    expect(labels).toHaveLength(4)
    expect(labels).toEqual(['44', '45', '46', '47'])
    expect(labels).not.toEqual(['2', '3', '4', '5']) // a re-indexed filtered view
  })

  it('a 5,000-row sheet renders a bounded number of rows', () => {
    renderSheet(sheet(5000), [3])

    const rendered = allRows().length
    expect(rendered).toBeGreaterThan(0)
    expect(rendered).toBeLessThanOrEqual(70) // ceil(1200/30) + 8 + 22, the clamp's hard ceiling
    expect(rendered).toBe(30)

    const top = parseInt(screen.getByTestId('sheet-spacer-top').style.height, 10)
    const bottom = parseInt(screen.getByTestId('sheet-spacer-bottom').style.height, 10)
    expect(top).toBe(0)
    expect(bottom).toBe(149100)
    expect(top + bottom + rendered * 30).toBe(150000)
  })

  it('scrolling moves the window without growing it', () => {
    renderSheet(sheet(1479), INVOICE_ROWS)

    const el = scrollBox()
    el.scrollTop = 30000
    fireEvent.scroll(el)

    const nums = sheetNumbers(allRows())
    expect(nums).toHaveLength(38)
    expect(Math.min(...nums)).toBe(994)
    expect(Math.max(...nums)).toBe(1031)
    expect(screen.getByTestId('sheet-spacer-top').style.height).toBe('29760px')
  })

  it('filtered mode shows the nothing-was-removed bar', async () => {
    renderSheet(sheet(1479), INVOICE_ROWS)
    expect(screen.queryByTestId('sheet-filtered-bar')).toBeNull() // floor

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))

    const bar = screen.getByTestId('sheet-filtered-bar')
    expect(bar.textContent).toContain("1,475 of the file's 1,479 rows are hidden by this view.")
    expect(bar.textContent).toContain('Nothing has been removed from the stored file — switch back to see them.')
    expect(bar.style.background).toBe('var(--status-amber-bg)')

    fireEvent.scroll(scrollBox())
    expect(screen.getByTestId('sheet-filtered-bar').textContent).toContain('Nothing has been removed')
    cleanup()

    // `hidden` comes off the TRUE total, not rows_returned: under truncation the two differ,
    // and the bar counting only the returned window would understate what it is hiding.
    renderSheet(sheet(5000, { rows_total: 5001, truncated: true }), INVOICE_ROWS)
    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))
    expect(screen.getByTestId('sheet-filtered-bar').textContent).toContain(
      "4,997 of the file's 5,001 rows are hidden by this view.",
    )
  })

  it('whole file is the default', () => {
    renderSheet(sheet(1479), INVOICE_ROWS)

    expect(screen.getByTestId('sheet-scope-file').getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByTestId('sheet-scope-invoice').getAttribute('aria-pressed')).toBe('false')
    expect(screen.queryByTestId('sheet-filtered-bar')).toBeNull()
    expect(screen.getAllByTestId('sheet-row').length).toBeGreaterThan(0)

    const status = screen.getByTestId('sheet-status').textContent
    expect(status).toBe('SHOWING ALL 1,479 ROWS')
    expect(status).not.toContain('SHEET') // the design's "· SHEET 1 OF 1" is unbackable
  })

  it("the invoice's rows are scrolled into view on open", () => {
    renderSheet(sheet(1479), INVOICE_ROWS)

    // Both halves of the pair: the element scrolled AND the window state followed it. jsdom
    // fires no scroll event on assignment, so only the second proves the rows actually render.
    expect(scrollBox().scrollTop).toBe(1170)
    expect(screen.getAllByTestId('sheet-row-marked')).toHaveLength(4)
  })

  it('an invoice with no recorded rows offers no jump control', () => {
    renderSheet(sheet(1479), null)

    const toolbar = screen.getByTestId('sheet-toolbar')
    expect((toolbar.textContent ?? '').length).toBeGreaterThan(0) // floor
    expect(toolbar.textContent).toContain('The rows of this file that became this invoice were not recorded.')

    expect(screen.queryByTestId('sheet-jump')).toBeNull()
    expect(screen.queryByTestId('sheet-scope-invoice')).toBeNull()
    expect(screen.queryAllByTestId('sheet-row-marked')).toHaveLength(0)
    expect(screen.getAllByTestId('sheet-row').length).toBeGreaterThan(0)
  })

  it('a truncated sheet states the truncation and the true total', () => {
    renderSheet(sheet(1479), INVOICE_ROWS)
    expect(screen.queryByTestId('sheet-truncation')).toBeNull() // floor
    cleanup()

    renderSheet(sheet(5000, { rows_total: 5001, truncated: true }), INVOICE_ROWS)

    const banner = screen.getByTestId('sheet-truncation')
    expect(banner.textContent).toContain('5,000')
    expect(banner.textContent).toContain('5,001')
    expect(screen.getByTestId('sheet-status').textContent).toBe('SHOWING 5,000 OF 5,001 ROWS')
  })

  it('an invoice whose rows fall outside the returned window says so', () => {
    // 5002, not 5001: rowsWithinSheet's boundary is rows_returned + 1, so 5001 is PRESENT.
    renderSheet(sheet(5000, { rows_total: 5001, truncated: true }), [5002, 5003])

    expect(screen.getByTestId('sheet-truncation').textContent).toContain(
      'ROWS 5002–5003 OF THIS INVOICE ARE NOT IN THE SHOWN WINDOW',
    )
    expect(screen.queryByTestId('sheet-jump')).toBeNull()
    expect(screen.queryAllByTestId('sheet-row-marked')).toHaveLength(0)
    expect(screen.queryByTestId('sheet-scope-invoice')).toBeNull()
    cleanup()

    renderSheet(sheet(5000, { rows_total: 5001, truncated: true }), [44, 5002])

    expect(screen.getByTestId('sheet-truncation').textContent).toContain(
      'ROW 5002 OF THIS INVOICE IS NOT IN THE SHOWN WINDOW',
    )
    const jump = screen.getByTestId('sheet-jump')
    expect(jump.textContent).toBe('Jump to ROW 44')
    expect(jump.textContent).not.toContain('5002')
  })

  it('the marker track draws the file, the invoice and the other invoices', () => {
    const others = Array.from({ length: 20 }, (_, i) => 100 + i * 11)
    renderSheet(sheet(1479), INVOICE_ROWS, others)

    expect(screen.getByTestId('sheet-marker-track')).toBeTruthy() // floor

    const blocks = screen.getAllByTestId('marker-invoice-block')
    expect(blocks).toHaveLength(1)
    expect(parseFloat(blocks[0].style.top)).toBeCloseTo(((44 - 2) / 1479) * 100, 2)
    expect(blocks[0].style.minHeight).toBe('4px')

    expect(screen.getAllByTestId('marker-tick')).toHaveLength(3) // indices 0 / 9 / 18

    const viewport = screen.getByTestId('marker-viewport')
    expect(Number.isFinite(parseFloat(viewport.style.top))).toBe(true)
  })

  it('clicking the marker track jumps the canvas', () => {
    renderSheet(sheet(1479), INVOICE_ROWS)

    const track = screen.getByTestId('sheet-marker-track')
    track.getBoundingClientRect = () =>
      ({ top: 0, bottom: 600, height: 600, left: 0, right: 16, width: 16, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect
    fireEvent.click(track, { clientY: 300 })

    expect(scrollBox().scrollTop).toBe(22200)
    const nums = sheetNumbers(allRows())
    expect(Math.min(...nums)).toBe(734)
    expect(Math.max(...nums)).toBe(771)
    cleanup()

    // The guard: jsdom's native rect is all zeros, and an unguarded divide writes NaN.
    renderSheet(sheet(1479), INVOICE_ROWS)
    fireEvent.click(screen.getByTestId('sheet-marker-track'), { clientY: 300 })
    expect(scrollBox().scrollTop).toBe(1170)
    expect(Number.isFinite(scrollBox().scrollTop)).toBe(true)
  })

  // The track is one coordinate system — the file — for all three markers. The rectangle is
  // therefore pinned against the FILE positions of the rows on screen, never against view
  // indices, which agree only when the view is the whole file.

  it('the viewport rectangle sits at the invoice, not the top, in filtered mode', async () => {
    renderSheet(sheet(1479), [744, 745, 746])
    // Whole-file scope opens scrolled to the invoice, so the rectangle is already ~49% down
    // and 38 rows tall — the filtered view must keep the position and collapse only the span.
    expect(parseFloat(screen.getByTestId('marker-viewport').style.height)).toBeCloseTo((38 / 1479) * 100, 2)

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))

    expect(sheetNumbers(allRows())).toEqual([744, 745, 746]) // floor: the filter really applied

    const viewport = screen.getByTestId('marker-viewport')
    expect(parseFloat(viewport.style.top)).toBeCloseTo((742 / 1479) * 100, 2)
    expect(parseFloat(viewport.style.top)).toBeGreaterThan(40) // not pinned to the track's head
    expect(parseFloat(viewport.style.height)).toBeCloseTo((3 / 1479) * 100, 2)

    // Every row on screen IS an invoice row, so the two markers must coincide exactly.
    expect(viewport.style.top).toBe(screen.getAllByTestId('marker-invoice-block')[0].style.top)
  })

  it('a discontiguous visible set draws one rectangle per run, not one spanning bar', async () => {
    renderSheet(sheet(1479), [44, 1400])

    expect(screen.getAllByTestId('marker-viewport')).toHaveLength(1) // floor: whole file is one run

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))
    expect(sheetNumbers(allRows())).toEqual([44, 1400]) // floor

    const runs = screen.getAllByTestId('marker-viewport')
    expect(runs).toHaveLength(2)
    expect(parseFloat(runs[0].style.top)).toBeCloseTo((42 / 1479) * 100, 2)
    expect(parseFloat(runs[1].style.top)).toBeCloseTo((1398 / 1479) * 100, 2)
    // Two rows on screen cover two rows of the file, never the 92% between them.
    for (const run of runs) expect(parseFloat(run.style.height)).toBeCloseTo((1 / 1479) * 100, 2)
  })

  it('the viewport rectangle never claims rows past the returned window', () => {
    renderSheet(sheet(5000, { rows_total: 5001, truncated: true }), INVOICE_ROWS)

    const el = scrollBox()
    el.scrollTop = 150000
    fireEvent.scroll(el)

    const nums = sheetNumbers(allRows())
    expect(nums).toHaveLength(8)
    expect(Math.max(...nums)).toBe(5001) // floor: the window really is at the returned tail

    const viewport = screen.getByTestId('marker-viewport')
    const top = parseFloat(viewport.style.top)
    const height = parseFloat(viewport.style.height)
    expect(top).toBeCloseTo((4992 / 5001) * 100, 2)
    expect(height).toBeCloseTo((8 / 5001) * 100, 2)
    // The strip's last sliver is row 5002 — stored, never sent, so nothing may cover it.
    expect(top + height).toBeCloseTo((5000 / 5001) * 100, 2)
    expect(top + height).toBeLessThan(100)
  })

  it('a truncated file in filtered mode still places the rectangle by file position', async () => {
    renderSheet(sheet(5000, { rows_total: 50000, truncated: true }), [3000, 3001])

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))
    expect(sheetNumbers(allRows())).toEqual([3000, 3001]) // floor

    const viewport = screen.getByTestId('marker-viewport')
    // 50,000 rows_total, 5,000 returned: view indices would put this at 0%, file space at ~6%.
    expect(parseFloat(viewport.style.top)).toBeCloseTo((2998 / 50000) * 100, 2)
    expect(parseFloat(viewport.style.top)).toBeGreaterThan(1)
  })

  it('clicking the track in filtered mode lands on the nearest visible row', async () => {
    renderSheet(sheet(1479), Array.from({ length: 61 }, (_, i) => 700 + i))

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))
    expect(scrollBox().scrollTop).toBe(0) // floor: the click, not the open, moved the canvas
    expect(sheetNumbers(allRows())[0]).toBe(700)

    const track = screen.getByTestId('sheet-marker-track')
    track.getBoundingClientRect = () =>
      ({ top: 0, bottom: 600, height: 600, left: 0, right: 16, width: 16, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect
    fireEvent.click(track, { clientY: 300 }) // half way down the FILE, i.e. sheet row 742

    expect(scrollBox().scrollTop).toBe(1260) // index 42 of the filtered rows, i.e. row 742
    expect(scrollBox().scrollTop).not.toBe(1800) // the last filtered row, where a clamp lands
    const nums = sheetNumbers(allRows())
    expect(Math.min(...nums)).toBe(734)
    expect(Math.max(...nums)).toBe(760)
  })

  it('clicking past the returned window clamps to the last row that was sent', () => {
    renderSheet(sheet(5000, { rows_total: 50000, truncated: true }), [44])

    const track = screen.getByTestId('sheet-marker-track')
    track.getBoundingClientRect = () =>
      ({ top: 0, bottom: 600, height: 600, left: 0, right: 16, width: 16, x: 0, y: 0, toJSON: () => ({}) }) as DOMRect
    fireEvent.click(track, { clientY: 480 }) // 80% down a file whose last 90% was never sent

    expect(scrollBox().scrollTop).toBe(149970) // index 4999 — sheet row 5001, the last one sent
    expect(Math.max(...sheetNumbers(allRows()))).toBe(5001)
  })

  it('the sheet never names a property the design system lacks', () => {
    // cwd, not import.meta.url: under jsdom the latter is an http: URL and fileURLToPath throws.
    const src = readFileSync(path.join(process.cwd(), 'src/components/SourceDocumentSheet.tsx'), 'utf8')
    expect(src.length).toBeGreaterThan(1000) // floor: the file really was read

    expect(src).not.toContain('--accent-tint')
    expect(src).toContain('var(--accent-10)')
    expect(src).toContain('inset 2px 0 0 var(--accent)')
  })

  it('the table scrolls horizontally like a spreadsheet', () => {
    renderSheet(sheet(1479), INVOICE_ROWS)

    expect(screen.getByTestId('sheet-grid').style.minWidth).toBe('min-content')

    const header = screen.getByTestId('sheet-header')
    expect(header.style.position).toBe('sticky')
    expect(header.style.top).toBe('0px')

    const headCells = Array.from(header.children) as HTMLElement[]
    expect(headCells).toHaveLength(COLUMNS.length + 1)
    expect(headCells[0].textContent).toBe('#')
    expect(headCells[0].style.width).toBe('56px')
    expect(headCells[1].style.width).toBe('106px')

    const dataCells = Array.from(rowByCell('INV-44').children) as HTMLElement[]
    expect(dataCells[0].style.width).toBe('56px')
    expect(dataCells[1].style.width).toBe('106px')
    expect(dataCells[1].style.textOverflow).toBe('ellipsis')
  })

  // Lives here, not in SourceDocumentModal.test.tsx, which another subtask's QA owns.
  it('the shell mounts the canvas for the spreadsheet state', async () => {
    const record: SourceDocumentRecord = {
      id: 'doc-1',
      filename: 'june-sales.xlsx',
      declared_content_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      size_bytes: 624640,
      content_hash: '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560',
      uploaded_at: '2026-06-12T11:42:00Z',
      uploaded_by: null,
      invoices_created: 500,
      other_invoice_rows: [100, 200],
    }
    const data: SourceDocumentResponse = { invoice_id: 'inv-1', source_rows: INVOICE_ROWS, document: record }
    const meta: SourceDocumentAsync = { status: 'ready', data, error: null, run: vi.fn() }

    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(sheet(60)) })),
    )

    const ctx = {
      authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
      getToken: () => 'tok',
      mode: 'firm',
      user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
      active: { name: 'Lagos Logistics Ltd' },
    } as unknown as PlatformCtx

    render(
      <SourceDocumentModal
        ctx={ctx}
        meta={meta}
        invoiceNumber="INV-2026-0037"
        invoiceCreatedAt="2026-06-12T09:15:00Z"
        createdBy={null}
        onClose={vi.fn()}
      />,
    )

    await screen.findAllByTestId('sheet-row-marked')
    const canvas = screen.getByTestId('source-document-canvas')
    expect(canvas.querySelectorAll('[data-testid="sheet-row-marked"]').length).toBeGreaterThan(0)
  })

  // --- QA adversarial coverage below ---

  it('a sheet with zero data rows renders no rows and no spacer height', () => {
    renderSheet(sheet(0), null)

    expect(allRows()).toHaveLength(0)
    expect(screen.getByTestId('sheet-spacer-top').style.height).toBe('0px')
    expect(screen.getByTestId('sheet-spacer-bottom').style.height).toBe('0px')
    expect(screen.getByTestId('sheet-status').textContent).toBe('SHOWING ALL 0 ROWS')
  })

  it('a sheet with exactly one data row renders it marked, with no spacer artifact', () => {
    renderSheet(sheet(1), [2])

    const rows = allRows()
    expect(rows).toHaveLength(1)
    expect(sheetNumbers(rows)).toEqual([2])
    expect(screen.getAllByTestId('sheet-row-marked')).toHaveLength(1)
    expect(screen.getByTestId('sheet-spacer-top').style.height).toBe('0px')
    expect(screen.getByTestId('sheet-spacer-bottom').style.height).toBe('0px')
  })

  it('a 30-row sheet (just at the window bound) renders every row', () => {
    renderSheet(sheet(30), null)

    expect(allRows()).toHaveLength(30)
    expect(screen.getByTestId('sheet-spacer-bottom').style.height).toBe('0px')
  })

  it('a 31-row sheet (just past the window bound) holds one row back', () => {
    renderSheet(sheet(31), null)

    expect(allRows()).toHaveLength(30)
    expect(screen.getByTestId('sheet-spacer-bottom').style.height).toBe('30px')
  })

  it('source_rows arriving unsorted still marks the right rows in sheet order', () => {
    renderSheet(sheet(1479), [47, 44]) // the CHECK constraint does not require sorted input

    const marked = screen.getAllByTestId('sheet-row-marked')
    expect(sheetNumbers(marked)).toEqual([44, 47]) // rendered in sheet order, not input order
    expect(screen.getByTestId('sheet-jump').textContent).toBe('Jump to ROWS 44 AND 47')
  })

  it('source_rows carrying a duplicate de-dupes for both marking and the invoice count', async () => {
    renderSheet(sheet(1479), [44, 44, 45]) // the CHECK constraint does not require distinct input

    expect(sheetNumbers(screen.getAllByTestId('sheet-row-marked'))).toEqual([44, 45])

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))
    expect(screen.getByTestId('sheet-scope-invoice').textContent).toBe('This invoice · 2 rows')
  })

  it('a short row (fewer cells than the header) renders only its own cells, no padding', () => {
    const s = sheet(3)
    s.rows[0] = ['INV-2', 'only-one-more-cell']
    renderSheet(s, null)

    const row = rowByCell('INV-2')
    expect(row.children).toHaveLength(1 + 2) // number cell + exactly the 2 cells this row has
  })

  it('a long row (more cells than the header) renders every cell, none dropped', () => {
    const s = sheet(3)
    const extra = Array.from({ length: COLUMNS.length + 4 }, (_, i) => `extra-${i}`)
    s.rows[1] = ['INV-3', ...extra]
    renderSheet(s, null)

    const row = rowByCell('INV-3')
    expect(row.children).toHaveLength(1 + 1 + extra.length) // number cell + INV-3 + every extra cell
  })

  it('a header with an empty-string column name still renders and reserves its width', () => {
    const s: DocumentSheet = {
      format: 'xlsx',
      delimiter: null,
      encoding: null,
      columns: ['Invoice No', '', 'Total'],
      rows: [['INV-2', 'x', '100']],
      rows_total: 1,
      rows_returned: 1,
      truncated: false,
    }
    renderSheet(s, null)

    const headCells = Array.from(screen.getByTestId('sheet-header').children) as HTMLElement[]
    expect(headCells).toHaveLength(4) // # + 3 columns, including the empty one
    expect(headCells[2].textContent).toBe('')
    expect(headCells[2].style.width).toBe('106px') // an empty name still reserves its column
  })

  it('never calls console.error for a truncated sheet, a not-recorded range, or an empty file', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})

    renderSheet(sheet(5000, { rows_total: 5001, truncated: true }), [5002, 5003])
    cleanup()
    renderSheet(sheet(1479), null)
    cleanup()
    renderSheet(sheet(0), null)

    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
  })

  it('toggling to filtered mode and back preserves row identity and its sheet number', async () => {
    renderSheet(sheet(1479), INVOICE_ROWS)
    const before = labelOf(rowByCell('INV-44'))

    await userEvent.click(screen.getByTestId('sheet-scope-invoice'))
    await userEvent.click(screen.getByTestId('sheet-scope-file'))

    const after = labelOf(rowByCell('INV-44'))
    expect(after).toBe(before)
    expect(after).toBe('44')
    expect(screen.getByTestId('sheet-scope-file').getAttribute('aria-pressed')).toBe('true')
  })
})
