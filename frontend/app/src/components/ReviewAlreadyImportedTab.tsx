// Review · "Already imported" tab (BUG-08-03). The DUPLICATE channel: rows the parser
// read perfectly, whose invoice this workspace already holds. Deliberately NOT a sibling
// of ReviewUnreadableTab's copy or palette — nothing here failed, so the muted family
// (the non-verdict one, MemberParts.tsx:100-104) is the correct family and no row is
// asked to be corrected.

import { alreadyImportedCsvAll, type AlreadyImportedRowAll } from '../lib/reviewBatch'

const ALREADY_IMPORTED_GRID = '150px 90px 1fr'

// Rendered as visible text AND as `title`; a disabled button is out of the tab order, so
// the visible sibling is the only layer a keyboard/SR user can reach.
const UNRESOLVED_REASON = 'The matching invoice was not recorded for this row.'

// Its own download, not extra rows on the unreadable export: that file's header would
// file these rows under "Why it could not be read" (alreadyImportedCsvAll's own note).
function downloadCsv(rows: AlreadyImportedRowAll[], batchIds: string[]): void {
  // BOM so Excel reads the em dash and non-ASCII supplier names as UTF-8, not the local
  // ANSI codepage — ReviewUnreadableTab.tsx:47's literal U+FEFF, byte-identical.
  const blob = new Blob([`﻿${alreadyImportedCsvAll(rows)}`], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `already-imported-rows-${batchIds.join('-')}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

export function ReviewAlreadyImportedTab({
  rows,
  rowsTotal,
  batchIds,
  onOpenInvoice,
}: {
  rows: AlreadyImportedRowAll[]
  rowsTotal: number
  batchIds: string[]
  // There is no invoice-detail URL in this SPA — the route-out is a callback
  // (types.ts:454, App.tsx:941), so the control is a button, never an <a href>.
  onOpenInvoice: (id: string) => void
}) {
  return (
    <div data-testid="review-already-imported-tab" style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ padding: '14px 16px', borderRadius: 'var(--radius-md)', background: 'var(--status-muted-bg)', border: '1px solid var(--status-muted-border)', color: 'var(--status-muted-text)' }}>
        <div style={{ fontSize: 13.5, fontWeight: 600, marginBottom: 5 }}>{rows.length} rows were already in your ledger</div>
        <p style={{ fontSize: 12.5, margin: 0, lineHeight: 1.55 }}>
          These rows match invoices this workspace already holds, so the import had nothing new to add. Nothing is wrong with them and there is nothing to correct.
        </p>
      </div>

      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div className="label" style={{ display: 'grid', gridTemplateColumns: ALREADY_IMPORTED_GRID, gap: 14, padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <span>File</span>
          <span>Row</span>
          <span>Invoice already in your ledger</span>
        </div>
        {rows.map((r, i) => {
          // Per-row, not the module-const id every other disabled-with-reason site uses:
          // ReviewRow.tsx:76-78's "at most one row is ever expanded" does not hold here,
          // where N rows render at once and any number of them can be unresolved.
          const reasonId = `already-imported-unresolved-reason-${i}`
          const invoiceId = r.invoiceId
          return (
            <div
              key={i}
              style={{ display: 'grid', gridTemplateColumns: ALREADY_IMPORTED_GRID, gap: 14, alignItems: 'baseline', padding: '11px 18px', borderTop: i === 0 ? 'none' : '1px solid var(--line-1)' }}
            >
              {/* Already resolved through filenameLabel's "source not recorded" fallback
                  by alreadyImportedRowsAll — never '', never the literal null. */}
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', wordBreak: 'break-all' }}>{r.file}</span>
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{r.row == null ? '—' : r.row}</span>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap' }}>
                {/* Disabled-with-reason, never hidden — InvoiceDetail.tsx:480-518's four
                    layers. The inline spread is disabled-only: on an enabled button it
                    would kill the legitimate :hover affordance. Ghost, so no
                    `filter: 'none'` — that neutraliser is .v2-btn-primary's alone. */}
                <button
                  onClick={invoiceId == null ? undefined : () => onOpenInvoice(invoiceId)}
                  disabled={invoiceId == null}
                  title={invoiceId == null ? UNRESOLVED_REASON : undefined}
                  aria-describedby={invoiceId == null ? reasonId : undefined}
                  className="v2-btn v2-btn-ghost pf-btn"
                  style={{
                    height: 30,
                    padding: '0 12px',
                    fontSize: 12.5,
                    ...(invoiceId == null ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                  }}
                >
                  View invoice
                </button>
                {invoiceId == null && (
                  <span id={reasonId} style={{ fontSize: 12, color: 'var(--fg-3)' }}>{UNRESOLVED_REASON}</span>
                )}
              </div>
            </div>
          )
        })}
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <button
          onClick={() => downloadCsv(rows, batchIds)}
          className="v2-btn v2-btn-ghost pf-btn"
          style={{ height: 36, padding: '0 14px', fontSize: 13 }}
        >
          Download this list (CSV)
        </button>
        <span style={{ marginLeft: 'auto', fontSize: 12.5, color: 'var(--fg-3)' }}>
          {rows.length} of {rowsTotal} rows were already in your ledger.
        </span>
      </div>
    </div>
  )
}
