// Review · "Unreadable rows" tab (INVCR-01-09, §7.4, Core AC 6). The STRUCTURAL
// channel: rows the parser could not turn into an invoice at all. No row here has an
// invoice id or a lifecycle state, and none can — `UnreadableRowAll` is `{row, column,
// message, file}` by type (BULK-01-07 adds `file`, over every batch in the run), so
// there is no field to put a lifecycle state in.
//
// DEGRADED VERSUS §7.4, AND IT SAYS SO. Two things §7.4 draws are not on the wire and
// are NOT built ([raw-source-line-dropped]):
//   - the raw semicolon-delimited source line under each row, and
//   - the offending-cell chip.
// §17's revised backend list supersedes §11 and omits §11.6; there is no raw line in any
// response, and adding one would mean persisting raw file content — PII, plus the 10 MiB
// upload cap. §7.4's five polished reason strings are [brief-only] for the same reason:
// the server sends its own (`rows disagree on issue_date`, `blank invoice number: row
// cannot be grouped`), and those are rendered VERBATIM. Nothing here authors a reason.
//
// `Column` is renamed `Field`. `RowError.field` is the IMPORTER's canonical field name
// (`invoice_number`, `issue_date`, `subtotal` — importer/service.go), not a heading from
// the user's spreadsheet, and labelling it `Column` sends them hunting for a column that
// is not there. The caption under the table states that plainly rather than leaving it to
// be inferred, and also states that the field is a best guess on numeric errors
// (service.go:346) — which is why an absent one renders `—` and never an invented name.
//
// "Download this list (CSV)", NOT §7.4's "Download these rows": with no raw line there
// are no ROWS to download. What the file contains is exactly this rendered table, which
// is still a genuinely useful fix-list to take back to Excel — but the noun in §7.4's
// label would be a lie about what lands in the user's downloads folder. The serialization
// is pure and spec'd (`unreadableCsvAll`, CSV-1 + BULK-06-… File-column siblings); only
// the Blob/anchor click lives here.

import { ENTITY_REQUIRED_REASON } from '../lib/invoiceDraft'
import { unreadableCsvAll, type ReviewUnit, type UnreadableRowAll } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'

// Widened by one column, File, at the front (BULK-01-07, AC #5) -- UNREADABLE_GRID had
// no such column when this tab could only ever show one batch's rows.
const UNREADABLE_GRID = '150px 90px 170px 1fr'

// The one DOM-only step: turn the pure CSV string into a file the browser saves. Kept as
// small as possible precisely because it has no unit oracle — everything decidable
// (quoting, the header row, the null-row cell) is decided inside unreadableCsvAll.
// `batchIds` (was a single `batchId`) names the download: `.join('-')` over a
// single-element array returns that element verbatim, so a single-batch run's filename
// stays byte-identical to the shipped one.
function downloadCsv(rows: UnreadableRowAll[], batchIds: string[], unit: ReviewUnit): void {
  // The BOM is what makes Excel read the em dash and any non-ASCII supplier name as
  // UTF-8 rather than the local ANSI codepage — the same mojibake class the importer's
  // own decoder exists to undo, and this file's whole purpose is to be opened in Excel.
  const blob = new Blob([`﻿${unreadableCsvAll(rows, unit)}`], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download =
    unit === 'document'
      ? `unreadable-documents-${batchIds.join('-')}.csv`
      : `unreadable-rows-${batchIds.join('-')}.csv`
  a.click()
  URL.revokeObjectURL(url)
}

export function ReviewUnreadableTab({
  ctx,
  rows,
  rowsTotal,
  batchIds,
  onImportCorrected,
  unit,
}: {
  // One prop, not a second `onEnterByHand` entry: it carries BOTH ctx.enterByHand and
  // ctx.activeEntity, exactly as ReviewInvoicesTab already takes it from the same shell.
  ctx: PlatformCtx
  rows: UnreadableRowAll[]
  // Summed across every batch in the run (BULK-01-07) -- was the one batch's own
  // `rows_total`.
  rowsTotal: number
  // Widened from a single `batchId` (BULK-01-07) -- the download filename can no
  // longer key off one id.
  batchIds: string[]
  onImportCorrected: () => void
  // Required and undefaulted: a caller that forgot it would ship a document run still
  // saying "rows", and only the compiler can see that (SW-4a, ReviewUnreadableTab.test.tsx).
  unit: ReviewUnit
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      {/* Amber panel — CreateUpload.tsx:203-226's shape. Amber, not red: nothing here
          failed a rule, so the red the compliance channel owns would mis-signal it. */}
      <div style={{ padding: '14px 16px', borderRadius: 'var(--radius-md)', background: 'var(--status-amber-bg)', border: '1px solid var(--status-amber-border)', color: 'var(--status-amber-text)' }}>
        <div style={{ fontSize: 13.5, fontWeight: 600, marginBottom: 5 }}>
          {unit === 'document' ? <>{rows.length} documents never became invoices</> : <>{rows.length} rows never became invoices</>}
        </div>
        <p style={{ fontSize: 12.5, margin: 0, lineHeight: 1.55 }}>
          {unit === 'document'
            ? 'The extractor could not read them, so no rule was ever run against them and nothing was stored. They cannot be fixed here: replace the documents and import again.'
            : 'The importer could not read them, so no rule was ever run against them and nothing was stored. They cannot be fixed here: correct the rows in your file and import again.'}
        </p>
      </div>

      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
        <div className="label" style={{ display: 'grid', gridTemplateColumns: UNREADABLE_GRID, gap: 14, padding: '10px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <span>File</span>
          {/* Two whole spans, not one span around a ternary — reviewCopy.census.test.ts
              (U4) needles each header cell as written. The em dash is what the cells
              below already render for a null row (SW-5, ReviewAlreadyImportedTab.test.tsx). */}
          {unit === 'document' ? <span>—</span> : <span>Row</span>}
          <span>Field</span>
          <span>Why it could not be read</span>
        </div>
        {rows.map((r, i) => {
          // Per-row, like ReviewAlreadyImportedTab's: N rows render at once and every one
          // of them is refused when no entity is resolved.
          const reasonId = `unreadable-handoff-reason-${i}`
          const documentId = r.documentId
          // The resolved-entity predicate every filing gate reads (fileDraftGate,
          // CreateUpload.tsx) — never a client id, which can be non-null before the
          // entity is fetched.
          const blocked = ctx.activeEntity === null
          return (
            <div
              key={i}
              // The row is the only handle a deployed spec has on WHICH document a hand-off
              // belongs to: the file label and the button are siblings inside it, and the
              // same filename also renders in the files strip above the tabs
              // (import-wizard.spec.ts's EXTR15-E2E-02).
              data-testid="unreadable-row"
              style={{ display: 'grid', gridTemplateColumns: UNREADABLE_GRID, gap: 14, alignItems: 'baseline', padding: '11px 18px', borderTop: i === 0 ? 'none' : '1px solid var(--line-1)' }}
            >
              {/* `file` resolves through the SAME "source not recorded" fallback as the
                  CSV export (unreadableRowsAll, lib/reviewBatch.ts) — never '', never the
                  literal null. */}
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', wordBreak: 'break-all' }}>{r.file}</span>
              {/* `row: null` is an em dash, never "ROW null" — the server told us it could
                  not attribute the failure to a line, and that is a fact worth stating. */}
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-3)' }}>{r.row == null ? '—' : r.row}</span>
              <span className="mono" style={{ fontSize: 12, color: 'var(--fg-2)', wordBreak: 'break-word' }}>{r.column}</span>
              {/* VERBATIM. No client-authored reason string, ever. */}
              <span style={{ fontSize: 13, color: 'var(--fg-1)' }}>
                {r.message}
                {/* Inside the "why" cell, so no new grid track (UT-5). A spreadsheet row
                    has no document to hand off, and neither has a document row whose
                    upload never reached storage — both render no control at all. */}
                {unit === 'document' && documentId !== null && (
                  <span style={{ display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap', marginTop: 8 }}>
                    {/* Disabled-with-reason, never hidden — ReviewAlreadyImportedTab.tsx's
                        four layers, and CreateFlow.tsx's DocumentFailureRow offers the
                        same hand-off on the import run's failure list. The inline spread
                        is disabled-only: on an enabled button it would kill the legitimate
                        :hover affordance. */}
                    <button
                      onClick={blocked ? undefined : () => ctx.enterByHand(documentId)}
                      disabled={blocked}
                      title={blocked ? ENTITY_REQUIRED_REASON : undefined}
                      aria-describedby={blocked ? reasonId : undefined}
                      className="v2-btn v2-btn-ghost pf-btn"
                      style={{
                        height: 30,
                        padding: '0 12px',
                        fontSize: 12.5,
                        flex: 'none',
                        ...(blocked ? { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' } : null),
                      }}
                    >
                      Enter it by hand
                    </button>
                    {blocked && (
                      <span id={reasonId} style={{ fontSize: 12, color: 'var(--fg-3)', flex: 'none' }}>{ENTITY_REQUIRED_REASON}</span>
                    )}
                  </span>
                )}
              </span>
            </div>
          )
        })}
        <div style={{ padding: '11px 18px', borderTop: '1px solid var(--line-1)', fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.55 }}>
          Field names are the importer&rsquo;s own, not your spreadsheet&rsquo;s headings, and are a best guess on numeric errors.
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <button
          onClick={() => downloadCsv(rows, batchIds, unit)}
          className="v2-btn v2-btn-ghost pf-btn"
          style={{ height: 36, padding: '0 14px', fontSize: 13 }}
        >
          Download this list (CSV)
        </button>
        <button
          onClick={onImportCorrected}
          className="v2-btn v2-btn-ghost pf-btn"
          style={{ height: 36, padding: '0 14px', fontSize: 13 }}
        >
          Import a corrected file
        </button>
        <span style={{ marginLeft: 'auto', fontSize: 12.5, color: 'var(--fg-3)' }}>
          {unit === 'document' ? (
            <>{rows.length} of {rowsTotal} documents. The invoices that did import are unaffected.</>
          ) : (
            <>{rows.length} of {rowsTotal} rows. The invoices that did import are unaffected.</>
          )}
        </span>
      </div>
    </div>
  )
}
