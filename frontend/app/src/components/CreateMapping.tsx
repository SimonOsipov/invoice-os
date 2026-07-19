// Create flow · step 2 — Map canonical invoice fields onto spreadsheet columns.
// Ported from Platform.dc.html ~L471-540 (markup) + ~L1549-1589 (render values).
//
// One spreadsheet row is one invoice LINE ITEM; rows group into invoices by the
// column mapped to `invoice_number`. Recognised columns arrive pre-placed and
// badged AUTO — except the invoice number, which is never guessed.
//
// Every column, sample cell and file fact on this screen now comes from the SERVER's
// preview response (M4-08-04, Core AC2) — the browser never parses the file. The whole
// derivation lives in previewColumns (lib/importFlow.ts) so it has a node oracle under
// the no-jsdom constraint and this component stays a dumb renderer with one call site.
//
// The guard below therefore requires `preview` and `importFile`, NOT the old
// FILE_DATA/SAMPLE_FILES fixture pair. Known transient consequence: the 'csv' entry of
// SAMPLE_FILES still routes here through parseFile with neither set, so it renders
// nothing until M4-08-06 deletes that entry and the fixture path with it.

import { CANON } from '../data'
import { recognize } from '../lib/mapping'
import { previewColumns } from '../lib/importFlow'
import { uploadPercent } from '../lib/importApi'
import { gripGlyph, shieldGlyph, tickGlyph13, xSmallGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

export function CreateMapping({ ctx }: { ctx: PlatformCtx }) {
  const { active, preview, importFile, mapping, armedField, dragField, uploadPhase, importError } = ctx
  if (!preview || !mapping || !importFile) return null

  const dropHot = !!(armedField || dragField)
  const recognized = recognize(preview.columns)

  // column header -> the field currently placed on it
  const colField: Record<string, string> = {}
  Object.keys(mapping).forEach((f) => {
    const h = mapping[f]
    if (h) colField[h] = f
  })

  // col.mappable is false for exactly one case: a blank-named column. '' is the
  // reserved unplaced sentinel toImportMapping strips, so a field dropped there would
  // vanish from the payload and import as NULL with no feedback at all — such a column
  // therefore gets no drop/click handler below. A whitespace-only header is NOT blocked:
  // the server's resolveMapping matches it exactly, so blocking it would be a
  // stricter-than-server gate.
  const columns = previewColumns(preview, 3).map((col) => {
    const fk = colField[col.header] || null
    const isAuto = !!fk && recognized[fk] === col.header
    return {
      ...col,
      field: fk,
      isAuto,
      colBg: fk ? (isAuto ? 'rgba(38,115,90,0.05)' : 'var(--accent-tint)') : 'var(--bg-2)',
      tagBg: isAuto ? 'var(--status-green-bg)' : 'var(--accent-tint)',
      tagBorder: isAuto ? 'var(--status-green-border)' : 'var(--accent)',
      tagColor: isAuto ? 'var(--status-green-text)' : 'var(--accent)',
      dropBorder: dropHot && !fk ? 'var(--accent)' : 'var(--line-2)',
      dropBg: dropHot && !fk ? 'var(--accent-tint)' : 'transparent',
    }
  })

  const fileExt = (() => {
    const dot = importFile.name.lastIndexOf('.')
    return dot > 0 ? importFile.name.slice(dot + 1).toUpperCase() : 'FILE'
  })()

  const percent = uploadPercent(uploadPhase)
  const uploading = uploadPhase.kind === 'sending' || uploadPhase.kind === 'processing'

  const paletteChips = CANON.filter((c) => !mapping[c.key]).map((c) => {
    const armed = armedField === c.key
    return {
      key: c.key,
      required: !!c.required,
      bg: armed ? 'var(--accent)' : c.required ? 'var(--status-red-bg)' : 'var(--bg-2)',
      border: armed ? 'var(--accent)' : c.required ? 'var(--status-red-border)' : 'var(--line-2)',
      color: armed ? '#fff' : c.required ? 'var(--status-red-text)' : 'var(--fg-1)',
    }
  })

  const leftToPlace = paletteChips.length
  const allPlaced = leftToPlace === 0
  const invNumMapped = !!mapping.invoice_number
  const optionalUnmapped = paletteChips.filter((c) => !c.required).length

  // Every fact here is the server's, including the nullable pair — delimiter/encoding
  // are JSON null for xlsx, and interpolating them raw would print the literal "null".
  const mapFacts = `DELIMITER ${preview.delimiter ?? '—'} · ${preview.encoding ?? '—'} · ${preview.rows_total} ROWS · ${preview.columns.length} COLS`
  // No invoice count: how many invoices these rows resolve to is the SERVER's verdict,
  // reported after the import, and computing it in the browser first is exactly the
  // duplicated-judgement this story removes (Core AC3). Rows are the honest unit here.
  const mapNote = !invNumMapped
    ? { text: 'Drag invoice_number onto a column to continue — the invoice number is never guessed for you.', color: 'var(--status-red-text)' }
    : optionalUnmapped > 0
      ? { text: `${optionalUnmapped} optional field${optionalUnmapped === 1 ? '' : 's'} still unplaced — unmapped fields import as empty and are judged by the rule engine.`, color: 'var(--status-muted-text)' }
      : { text: 'All fields mapped.', color: 'var(--status-green-text)' }
  const continueBtn = {
    bg: invNumMapped ? 'var(--accent)' : 'var(--bg-3)',
    color: invNumMapped ? '#fff' : 'var(--fg-4)',
    cursor: invNumMapped ? 'pointer' : 'not-allowed',
    label: invNumMapped ? `Import ${preview.rows_total} rows` : 'Map invoice number to continue',
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
        <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <span style={{ fontSize: 15, fontWeight: 600 }}>Map fields to columns · {active.short}</span>
          {allPlaced ? (
            <span className="mono" style={{ fontSize: 11, color: 'var(--status-green-text)' }}>ALL FIELDS PLACED</span>
          ) : (
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{leftToPlace} TO PLACE</span>
          )}
        </div>
        <div style={{ padding: '14px 20px 18px' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 9, padding: '11px 12px', background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 6, marginBottom: 14 }}>
            <span style={{ flex: 'none', color: 'var(--fg-3)', marginTop: 1 }}>{shieldGlyph}</span>
            <p style={{ fontSize: 12, color: 'var(--fg-2)', margin: 0, lineHeight: 1.5 }}>
              Drag each field onto the column that holds its data — or click a field, then a column. One spreadsheet row is a single line item; rows group into invoices by the column mapped to{' '}
              <span className="mono" style={{ fontSize: 11 }}>invoice_number</span>. Supplier details come from {active.short}, not the file. Recognised columns are pre-placed and marked{' '}
              <span className="mono" style={{ fontSize: 10, color: 'var(--status-green-text)' }}>AUTO</span> — the invoice number is never guessed.
            </p>
          </div>
          {allPlaced ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--status-green-text)' }}>
              <span style={{ display: 'inline-flex' }}>{tickGlyph13}</span> Every field is placed on a column.
            </div>
          ) : (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 9 }}>
              {paletteChips.map((c) => (
                <button
                  key={c.key}
                  draggable
                  onDragStart={(e) => {
                    try {
                      e.dataTransfer.setData('text/plain', c.key)
                      e.dataTransfer.effectAllowed = 'move'
                    } catch {
                      /* dataTransfer unavailable — click-to-place still works */
                    }
                    ctx.setDrag(c.key)
                  }}
                  onDragEnd={() => ctx.endDrag()}
                  onClick={() => ctx.armField(c.key)}
                  className="pf-btn"
                  style={{ display: 'inline-flex', alignItems: 'center', gap: 7, cursor: 'grab', fontFamily: 'var(--font-mono)', fontSize: 11.5, fontWeight: 600, letterSpacing: '0.02em', textTransform: 'uppercase', padding: '8px 12px', borderRadius: 6, background: c.bg, border: `1px solid ${c.border}`, color: c.color }}
                >
                  <span style={{ display: 'inline-flex', opacity: 0.6 }}>{gripGlyph}</span>
                  {c.key}
                  {c.required && <span style={{ fontWeight: 700 }}>*</span>}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, overflow: 'hidden' }}>
        <div style={{ padding: '12px 20px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 10, minWidth: 0 }}>
            <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 6, background: 'var(--bg-3)', color: 'var(--fg-3)', display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 8.5, fontWeight: 700 }}>{fileExt}</span>
            <span style={{ fontSize: 13.5, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{importFile.name}</span>
          </span>
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.03em', whiteSpace: 'nowrap' }}>{mapFacts}</span>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <div style={{ display: 'flex', minWidth: 'min-content' }}>
            {columns.map((col, ci) => (
              <div
                // Keyed by INDEX, not header: the preview returns headers verbatim and
                // duplicates are preserved, so a header key would collide and
                // mis-associate drop targets between two columns of the same name.
                key={ci}
                onDrop={
                  col.mappable
                    ? (e) => {
                        e.preventDefault()
                        ctx.dropOn(col.header)
                      }
                    : undefined
                }
                onDragOver={col.mappable ? (e) => e.preventDefault() : undefined}
                onClick={col.mappable ? () => ctx.clickCol(col.header) : undefined}
                style={{ flex: 'none', width: 150, borderRight: '1px solid var(--line-1)', background: col.colBg, cursor: col.mappable ? 'pointer' : 'not-allowed', opacity: col.mappable ? 1 : 0.6 }}
              >
                <div style={{ height: 22, display: 'grid', placeItems: 'center', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-4)', fontWeight: 600 }}>{col.letter}</span>
                </div>
                <div style={{ padding: '8px 9px', borderBottom: '1px solid var(--line-1)', minHeight: 66 }}>
                  <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', marginBottom: 6 }}>{col.header}</div>
                  {!col.mappable ? (
                    <div style={{ display: 'grid', placeItems: 'center', height: 30, border: '1px dashed var(--line-2)', borderRadius: 5 }}>
                      <span style={{ fontSize: 10, color: 'var(--fg-4)', textAlign: 'center', lineHeight: 1.3 }}>unnamed — not mappable</span>
                    </div>
                  ) : col.field ? (
                    <span
                      draggable
                      onDragStart={(e) => {
                        try {
                          e.dataTransfer.setData('text/plain', col.field as string)
                          e.dataTransfer.effectAllowed = 'move'
                        } catch {
                          /* dataTransfer unavailable — click-to-place still works */
                        }
                        ctx.setDrag(col.field as string)
                      }}
                      style={{ display: 'inline-flex', alignItems: 'center', gap: 5, maxWidth: '100%', cursor: 'grab', background: col.tagBg, border: `1px solid ${col.tagBorder}`, borderRadius: 5, padding: '3px 6px' }}
                    >
                      <span className="mono" style={{ fontSize: 9, fontWeight: 700, color: col.tagColor, letterSpacing: '0.01em', textTransform: 'uppercase', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{col.field}</span>
                      {col.isAuto && (
                        <span className="mono" style={{ flex: 'none', fontSize: 7.5, fontWeight: 700, color: 'var(--status-green-text)', border: '1px solid var(--status-green-border)', borderRadius: 3, padding: '0 3px' }}>AUTO</span>
                      )}
                      <span
                        onClick={(e) => {
                          e.stopPropagation()
                          ctx.unmap(col.header)
                        }}
                        style={{ flex: 'none', cursor: 'pointer', color: col.tagColor, display: 'inline-flex' }}
                      >
                        {xSmallGlyph}
                      </span>
                    </span>
                  ) : (
                    <div style={{ display: 'grid', placeItems: 'center', height: 30, border: `1px dashed ${col.dropBorder}`, borderRadius: 5, background: col.dropBg }}>
                      <span style={{ fontSize: 10.5, color: 'var(--fg-4)' }}>drop field</span>
                    </div>
                  )}
                </div>
                {col.samples.map((v, i) => (
                  <div key={i} style={{ height: 30, display: 'flex', alignItems: 'center', padding: '0 9px', borderBottom: '1px solid var(--line-1)' }}>
                    <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{v}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: '14px 20px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 20, minWidth: 0 }}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8, flex: 'none' }}>
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>Rows to import</span>
            <span className="mono" style={{ fontSize: 15, fontWeight: 700, color: 'var(--accent)' }}>{preview.rows_total}</span>
          </span>
          {importError ? (
            <span style={{ fontSize: 12, color: 'var(--status-red-text)', lineHeight: 1.4 }}>{importError.message}</span>
          ) : (
            <span style={{ fontSize: 12, color: mapNote.color, lineHeight: 1.4 }}>{mapNote.text}</span>
          )}
        </div>
        <div style={{ display: 'flex', gap: 10, flex: 'none', alignItems: 'center' }}>
          {/* Two-phase, honest progress: a determinate bar ONLY while the transport
              reports a computable byte total, then an indeterminate spinner once the
              last byte is away — everything after that (server parse, DB writes, rule
              evaluation) is unobservable, so there is no stage list to show. A run that
              never fires a computable progress event simply spins the whole time. */}
          {uploading &&
            (percent !== null ? (
              <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ width: 90, height: 5, borderRadius: 99, background: 'var(--bg-3)', overflow: 'hidden' }}>
                  <span style={{ display: 'block', height: '100%', width: `${percent}%`, background: 'var(--accent)' }} />
                </span>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>{percent}%</span>
              </span>
            ) : (
              <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ width: 13, height: 13, borderRadius: 99, border: '2px solid var(--bg-3)', borderTopColor: 'var(--accent)', display: 'block', animation: 'spin 0.7s linear infinite' }} />
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>Working…</span>
              </span>
            ))}
          <button onClick={ctx.backToImport} disabled={uploading} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 42, padding: '0 16px' }}>
            ← Back to import
          </button>
          <button
            onClick={ctx.continueMapping}
            disabled={uploading}
            className="v2-btn pf-btn"
            style={{ height: 42, padding: '0 18px', justifyContent: 'center', background: continueBtn.bg, color: continueBtn.color, cursor: continueBtn.cursor }}
          >
            {continueBtn.label}
          </button>
        </div>
      </div>
    </div>
  )
}
