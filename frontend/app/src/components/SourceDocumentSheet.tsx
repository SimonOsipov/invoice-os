// The spreadsheet canvas: the whole file, windowed, with this invoice's rows marked.
//
// It fetches nothing — SourceDocumentModal already holds the sheet, the invoice's rows and
// the sibling invoices' rows, and passes all three down.

import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent } from 'react'

import { fmtPlain } from '../lib/format'
import {
  contiguousRanges,
  describeSourceRows,
  numberSheetRows,
  rangeLabel,
  rowsWithinSheet,
  sheetWindow,
  type DocumentSheet,
  type NumberedRow,
} from '../lib/sourceDocument'

const ROW_H = 30
const GUTTER_W = 56
const CELL_W = 106
const TRACK_W = 16
/** The invoice's first row lands three rows below the top edge, not flush against it. */
const LEAD_IN_ROWS = 3
/** design §2.2, "sample every 9th group". */
const TICK_SAMPLE = 9

type Scope = 'file' | 'invoice'

const TOOLBAR: CSSProperties = {
  flex: 'none',
  padding: '9px 14px',
  borderBottom: '1px solid var(--line-1)',
  background: 'var(--bg-2)',
  display: 'flex',
  alignItems: 'center',
  gap: 10,
}

/** design §6: without these the toolbar labels wrap at narrow widths. */
const CONTROL: CSSProperties = { whiteSpace: 'nowrap', flex: 'none' }

const SEGMENT: CSSProperties = {
  ...CONTROL,
  height: 26,
  padding: '0 12px',
  // A plain button, never `.pf-btn`/`.pf-chip`: both force `border-radius` with `!important`.
  borderRadius: 999,
  border: '1px solid transparent',
  fontFamily: 'var(--font-sans)',
  fontSize: 12.5,
  fontWeight: 500,
  cursor: 'pointer',
}

const BANNER: CSSProperties = {
  flex: 'none',
  padding: '8px 14px',
  background: 'var(--status-amber-bg)',
  border: '1px solid var(--status-amber-border)',
  color: 'var(--status-amber-text)',
  fontSize: 12.5,
  lineHeight: 1.55,
}

const HEAD_CELL: CSSProperties = {
  flex: 'none',
  padding: '0 8px',
  fontSize: 10.5,
  fontWeight: 600,
  letterSpacing: '0.05em',
  color: 'var(--fg-3)',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
}

const CELL: CSSProperties = {
  flex: 'none',
  padding: '0 8px',
  fontSize: 12,
  color: 'var(--fg-2)',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
}

function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many
}

/** The marker track is the whole file, so every offset divides by rows_total. */
function pct(n: number, total: number): number {
  if (!(total > 0)) return 0
  return Math.min(100, Math.max(0, (n / total) * 100))
}

/** numberSheetRows starts the data at sheet row 2, so file index 0 is sheet row 2. */
const FIRST_DATA_SHEET_ROW = 2

/** Nearest on-screen row to a file position; past the returned window that is the last row sent. */
function nearestVisibleIndex(rows: NumberedRow[], sheetRow: number): number {
  let best = 0
  for (let i = 1; i < rows.length; i += 1) {
    if (Math.abs(rows[i].sheetRow - sheetRow) < Math.abs(rows[best].sheetRow - sheetRow)) best = i
  }
  return best
}

export function SourceDocumentSheet({
  sheet,
  sourceRows,
  otherInvoiceRows,
}: {
  sheet: DocumentSheet
  /** null = never recorded, distinct from []. */
  sourceRows: number[] | null
  /** Sibling invoices' first rows on the same document — already fetched by the shell. */
  otherInvoiceRows: number[]
}) {
  const [scope, setScope] = useState<Scope>('file')
  const [scrollTop, setScrollTop] = useState(0)
  const [viewportH, setViewportH] = useState(0)
  const scrollRef = useRef<HTMLDivElement | null>(null)

  // Bound once, BEFORE any scope filtering: that is what makes the sheet number structural
  // rather than a re-indexed position. No arithmetic on a slice offset may replace it.
  const numbered = useMemo(() => numberSheetRows(sheet.rows), [sheet.rows])

  const { present, missing } = useMemo(
    () => rowsWithinSheet(sourceRows, sheet.rows_returned),
    [sourceRows, sheet.rows_returned],
  )
  const presentSet = useMemo(() => new Set(present), [present])
  const ranges = useMemo(() => contiguousRanges(sourceRows), [sourceRows])
  const viewRows = useMemo(
    () => (scope === 'invoice' ? numbered.filter((r) => presentSet.has(r.sheetRow)) : numbered),
    [scope, numbered, presentSet],
  )

  const total = sheet.rows_total
  // The file fact, not present.length — the truncation banner carries any discrepancy.
  const invoiceRowCount = sourceRows ? new Set(sourceRows).size : 0
  const recorded = ranges.length > 0

  useLayoutEffect(() => {
    setViewportH(scrollRef.current?.clientHeight ?? 0)
  }, [sheet, scope])

  const presentKey = present.join(',')
  useEffect(() => {
    if (present.length === 0) return
    const el = scrollRef.current
    if (!el) return
    const first = viewRows.findIndex((r) => r.sheetRow === present[0])
    if (first < 0) return
    const top = Math.max(0, (first - LEAD_IN_ROWS) * ROW_H)
    // Both halves, always: no scroll event fires on assignment, so setting only the element
    // leaves the window at {0,30} and renders none of the rows we just scrolled to.
    el.scrollTop = top
    setScrollTop(top)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, sheet, presentKey])

  const { start, end } = sheetWindow(scrollTop, viewportH, viewRows.length)

  // File-space like the invoice block and the ticks: view indices over rows_total agree only
  // when the view IS the whole file. One run per contiguous stretch, so a filtered view of
  // rows 44 and 1400 draws two slivers, not one bar claiming the 92% between them.
  const viewRuns = contiguousRanges(viewRows.slice(start, end).map((r) => r.sheetRow))

  function scrollToIndex(index: number) {
    const top = Math.max(0, index * ROW_H)
    const el = scrollRef.current
    if (el) el.scrollTop = top
    setScrollTop(top)
  }

  function onTrackClick(e: MouseEvent<HTMLDivElement>) {
    const r = e.currentTarget.getBoundingClientRect()
    // An unlaid-out track returns an all-zero rect; the division would poison scrollTop with NaN.
    if (!(r.height > 0)) return
    const target = Math.round(((e.clientY - r.top) / r.height) * total) + FIRST_DATA_SHEET_ROW
    scrollToIndex(nearestVisibleIndex(viewRows, target))
  }

  function onJump() {
    const first = viewRows.findIndex((r) => r.sheetRow === present[0])
    if (first >= 0) scrollToIndex(Math.max(0, first - LEAD_IN_ROWS))
  }

  const hidden = total - present.length
  const jumpLabel = rangeLabel(present)
  const missingLabel = rangeLabel(missing)

  let status: string
  if (scope === 'invoice') status = `SHOWING ${jumpLabel} OF ${fmtPlain(total)}`
  else if (sheet.truncated) status = `SHOWING ${fmtPlain(sheet.rows_returned)} OF ${fmtPlain(total)} ROWS`
  else status = `SHOWING ALL ${fmtPlain(total)} ${plural(total, 'ROW', 'ROWS')}`

  const ticks = otherInvoiceRows.filter((_, i) => i % TICK_SAMPLE === 0)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      <div data-testid="sheet-toolbar" style={TOOLBAR}>
        <div
          style={{ ...CONTROL, display: 'flex', alignItems: 'center', gap: 2, padding: 2, background: 'var(--bg-1)', border: '1px solid var(--line-2)', borderRadius: 999 }}
        >
          <button
            type="button"
            data-testid="sheet-scope-file"
            aria-pressed={scope === 'file'}
            onClick={() => setScope('file')}
            style={{ ...SEGMENT, background: scope === 'file' ? 'var(--action)' : 'transparent', color: scope === 'file' ? 'var(--text-on-dark)' : 'var(--fg-2)' }}
          >
            {`Whole file · ${fmtPlain(total)} ${plural(total, 'row', 'rows')}`}
          </button>
          {present.length > 0 && (
            <button
              type="button"
              data-testid="sheet-scope-invoice"
              aria-pressed={scope === 'invoice'}
              onClick={() => setScope('invoice')}
              style={{ ...SEGMENT, background: scope === 'invoice' ? 'var(--action)' : 'transparent', color: scope === 'invoice' ? 'var(--text-on-dark)' : 'var(--fg-2)' }}
            >
              {`This invoice · ${fmtPlain(invoiceRowCount)} ${plural(invoiceRowCount, 'row', 'rows')}`}
            </button>
          )}
        </div>

        {!recorded && (
          <span style={{ ...CONTROL, fontSize: 12.5, color: 'var(--fg-3)' }}>
            {describeSourceRows(sourceRows, 'spreadsheet')}
          </span>
        )}

        {/* Labelled off `present`, never off source_rows: jumping to a row the server never
            returned is a broken control. */}
        {scope === 'file' && jumpLabel != null && (
          <button
            type="button"
            data-testid="sheet-jump"
            onClick={onJump}
            style={{ ...CONTROL, height: 28, padding: '0 12px', borderRadius: 'var(--radius-sm)', border: '1px solid var(--line-2)', background: 'var(--bg-1)', color: 'var(--fg-2)', fontFamily: 'var(--font-sans)', fontSize: 12.5, cursor: 'pointer' }}
          >
            {`Jump to ${jumpLabel}`}
          </button>
        )}

        <span
          className="mono"
          data-testid="sheet-status"
          style={{ ...CONTROL, marginLeft: 'auto', fontSize: 10.5, letterSpacing: '0.05em', color: 'var(--fg-3)' }}
        >
          {status}
        </span>
      </div>

      <div style={{ flex: 'none' }}>
        {scope === 'invoice' && (
          <div data-testid="sheet-filtered-bar" style={BANNER}>
            {`Filtered view — ${fmtPlain(hidden)} of the file's ${fmtPlain(total)} ${plural(total, 'row', 'rows')} ${plural(hidden, 'is', 'are')} hidden by this view. Nothing has been removed from the stored file — switch back to see them.`}
          </div>
        )}
        {sheet.truncated && (
          <div data-testid="sheet-truncation" style={BANNER}>
            {`This file has ${fmtPlain(total)} rows. The previewer shows the first ${fmtPlain(sheet.rows_returned)}; the rest are stored but not displayed here.`}
            {missingLabel != null && (
              <div className="mono" style={{ marginTop: 5, fontSize: 10.5, letterSpacing: '0.05em' }}>
                {`${missingLabel} OF THIS INVOICE ${plural(missing.length, 'IS', 'ARE')} NOT IN THE SHOWN WINDOW`}
              </div>
            )}
          </div>
        )}
      </div>

      <div style={{ display: 'flex', flex: 1, minHeight: 0 }}>
        <div
          ref={scrollRef}
          data-testid="sheet-scroll"
          onScroll={(e) => {
            setScrollTop(e.currentTarget.scrollTop)
            setViewportH(e.currentTarget.clientHeight)
          }}
          style={{ flex: 1, overflow: 'auto', position: 'relative', minWidth: 0 }}
        >
          <div data-testid="sheet-grid" style={{ minWidth: 'min-content' }}>
            <div
              data-testid="sheet-header"
              style={{ position: 'sticky', top: 0, zIndex: 1, display: 'flex', alignItems: 'center', height: ROW_H, background: 'var(--bg-2)', borderBottom: '1px solid var(--line-2)' }}
            >
              <div className="mono" style={{ ...HEAD_CELL, width: GUTTER_W }}>
                #
              </div>
              {sheet.columns.map((c, i) => (
                <div key={i} style={{ ...HEAD_CELL, width: CELL_W }}>
                  {c}
                </div>
              ))}
            </div>

            <div data-testid="sheet-spacer-top" style={{ height: start * ROW_H }} />

            {viewRows.slice(start, end).map((row) => {
              const marked = presentSet.has(row.sheetRow)
              return (
                <div
                  key={row.sheetRow}
                  data-sheet-row={row.sheetRow}
                  data-testid={marked ? 'sheet-row-marked' : 'sheet-row'}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    height: ROW_H,
                    borderBottom: '1px solid var(--line-1)',
                    ...(marked ? { background: 'var(--accent-10)', boxShadow: 'inset 2px 0 0 var(--accent)' } : null),
                  }}
                >
                  <div
                    className="mono"
                    data-testid="sheet-row-number"
                    style={{ ...CELL, width: GUTTER_W, color: marked ? 'var(--accent)' : 'var(--fg-4)' }}
                  >
                    {row.sheetRow}
                  </div>
                  {/* Ragged rows stay ragged: a short row simply ends early. */}
                  {row.cells.map((cell, i) => (
                    <div key={i} style={{ ...CELL, width: CELL_W }}>
                      {cell}
                    </div>
                  ))}
                </div>
              )
            })}

            <div data-testid="sheet-spacer-bottom" style={{ height: (viewRows.length - end) * ROW_H }} />
          </div>
        </div>

        {/* Sibling of the scroll container, so the track never scrolls with the sheet. */}
        <div
          data-testid="sheet-marker-track"
          onClick={onTrackClick}
          style={{ flex: 'none', width: TRACK_W, background: 'var(--bg-1)', borderLeft: '1px solid var(--line-1)', position: 'relative', cursor: 'pointer' }}
        >
          {ranges.map(([from, to]) => (
            <div
              key={from}
              data-testid="marker-invoice-block"
              style={{ position: 'absolute', left: 0, right: 0, top: `${pct(from - FIRST_DATA_SHEET_ROW, total)}%`, height: `${pct(to - from + 1, total)}%`, minHeight: 4, background: 'var(--accent)' }}
            />
          ))}
          {viewRuns.map(([from, to]) => (
            <div
              key={from}
              data-testid="marker-viewport"
              style={{ position: 'absolute', left: 0, right: 0, top: `${pct(from - FIRST_DATA_SHEET_ROW, total)}%`, height: `${pct(to - from + 1, total)}%`, minHeight: 4, background: 'var(--action-tint)', border: '1px solid var(--action)' }}
            />
          ))}
          {ticks.map((n, i) => (
            <div
              key={i}
              data-testid="marker-tick"
              style={{ position: 'absolute', left: 0, right: 0, top: `${pct(n - FIRST_DATA_SHEET_ROW, total)}%`, height: 1, background: 'var(--line-3)' }}
            />
          ))}
        </div>
      </div>
    </div>
  )
}
