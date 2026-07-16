// Create flow · step 2 — Map canonical invoice fields onto spreadsheet columns.
// Ported from Platform.dc.html ~L471-540 (markup) + ~L1549-1589 (render values).
//
// One spreadsheet row is one invoice LINE ITEM; rows group into invoices by the
// column mapped to `invoice_number`. Recognised columns arrive pre-placed and
// badged AUTO — except the invoice number, which is never guessed.

import { CANON, FILE_DATA, SAMPLE_FILES } from '../data'
import { recognize, groupInvoices } from '../lib/mapping'
import { gripGlyph, shieldGlyph, tickGlyph13, xSmallGlyph } from '../glyphs'
import type { PlatformCtx } from '../types'

export function CreateMapping({ ctx }: { ctx: PlatformCtx }) {
  const { active, uploadFile, mapping, armedField, dragField } = ctx
  const fileData = uploadFile ? FILE_DATA[uploadFile] : null
  const selFile = SAMPLE_FILES.find((f) => f.id === uploadFile) || null
  if (!fileData || !mapping || !selFile) return null

  const dropHot = !!(armedField || dragField)
  const recognized = recognize(fileData.headers)

  // column header -> the field currently placed on it
  const colField: Record<string, string> = {}
  Object.keys(mapping).forEach((f) => {
    const h = mapping[f]
    if (h) colField[h] = f
  })

  const columns = fileData.headers.map((h, ci) => {
    const fk = colField[h] || null
    const isAuto = !!fk && recognized[fk] === h
    return {
      header: h,
      letter: String.fromCharCode(65 + ci),
      field: fk,
      isAuto,
      samples: fileData.rows.slice(0, 3).map((r) => r[h]),
      colBg: fk ? (isAuto ? 'rgba(38,115,90,0.05)' : 'var(--accent-tint)') : 'var(--bg-2)',
      tagBg: isAuto ? 'var(--status-green-bg)' : 'var(--accent-tint)',
      tagBorder: isAuto ? 'var(--status-green-border)' : 'var(--accent)',
      tagColor: isAuto ? 'var(--status-green-text)' : 'var(--accent)',
      dropBorder: dropHot && !fk ? 'var(--accent)' : 'var(--line-2)',
      dropBg: dropHot && !fk ? 'var(--accent-tint)' : 'transparent',
    }
  })

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
  const invCount = invNumMapped ? groupInvoices(uploadFile, mapping).length : 0
  const optionalUnmapped = paletteChips.filter((c) => !c.required).length

  const mapFacts = `DELIMITER ${fileData.delimiter} · ${fileData.encoding} · ${fileData.rows.length} ROWS · ${fileData.headers.length} COLS`
  const mapNote = !invNumMapped
    ? { text: 'Drag invoice_number onto a column to continue — the invoice number is never guessed for you.', color: 'var(--status-red-text)' }
    : optionalUnmapped > 0
      ? { text: `${optionalUnmapped} optional field${optionalUnmapped === 1 ? '' : 's'} still unplaced — unmapped fields will fail validation at step 4.`, color: 'var(--status-muted-text)' }
      : { text: `All fields mapped · ${invCount} invoices ready to build.`, color: 'var(--status-green-text)' }
  const continueBtn = {
    bg: invNumMapped ? 'var(--accent)' : 'var(--bg-3)',
    color: invNumMapped ? '#fff' : 'var(--fg-4)',
    cursor: invNumMapped ? 'pointer' : 'not-allowed',
    label: invNumMapped ? `Continue · ${invCount} invoice${invCount === 1 ? '' : 's'}` : 'Map invoice number to continue',
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
            <span style={{ flex: 'none', width: 30, height: 30, borderRadius: 6, background: selFile.iconBg, color: selFile.iconColor, display: 'grid', placeItems: 'center', fontFamily: 'var(--font-mono)', fontSize: 8.5, fontWeight: 700 }}>{selFile.ext}</span>
            <span style={{ fontSize: 13.5, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{selFile.name}</span>
          </span>
          <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.03em', whiteSpace: 'nowrap' }}>{mapFacts}</span>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <div style={{ display: 'flex', minWidth: 'min-content' }}>
            {columns.map((col) => (
              <div
                key={col.header}
                onDrop={(e) => {
                  e.preventDefault()
                  ctx.dropOn(col.header)
                }}
                onDragOver={(e) => e.preventDefault()}
                onClick={() => ctx.clickCol(col.header)}
                style={{ flex: 'none', width: 150, borderRight: '1px solid var(--line-1)', background: col.colBg, cursor: 'pointer' }}
              >
                <div style={{ height: 22, display: 'grid', placeItems: 'center', background: 'var(--bg-1)', borderBottom: '1px solid var(--line-1)' }}>
                  <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-4)', fontWeight: 600 }}>{col.letter}</span>
                </div>
                <div style={{ padding: '8px 9px', borderBottom: '1px solid var(--line-1)', minHeight: 66 }}>
                  <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', marginBottom: 6 }}>{col.header}</div>
                  {col.field ? (
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
            <span style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>Invoices detected</span>
            <span className="mono" style={{ fontSize: 15, fontWeight: 700, color: 'var(--accent)' }}>{invNumMapped ? String(invCount) : '—'}</span>
          </span>
          <span style={{ fontSize: 12, color: mapNote.color, lineHeight: 1.4 }}>{mapNote.text}</span>
        </div>
        <div style={{ display: 'flex', gap: 10, flex: 'none' }}>
          <button onClick={ctx.backToImport} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 42, padding: '0 16px' }}>
            ← Back to import
          </button>
          <button onClick={ctx.continueMapping} className="v2-btn pf-btn" style={{ height: 42, padding: '0 18px', justifyContent: 'center', background: continueBtn.bg, color: continueBtn.color, cursor: continueBtn.cursor }}>
            {continueBtn.label}
          </button>
        </div>
      </div>
    </div>
  )
}
